package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/events"
	"github.com/ecakeman/cakerdesk/internal/store"
	"github.com/ecakeman/cakerdesk/internal/tools"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool             *pgxpool.Pool
	q                *store.Queries
	events           *events.Service
	leaseSeconds     int32
	heartbeatSeconds int32
	claimMaxWait     time.Duration
}

func NewService(pool *pgxpool.Pool, ev *events.Service, leaseSeconds, heartbeatSeconds int, claimMaxWait time.Duration) *Service {
	return &Service{
		pool:             pool,
		q:                store.New(pool),
		events:           ev,
		leaseSeconds:     int32(leaseSeconds),
		heartbeatSeconds: int32(heartbeatSeconds),
		claimMaxWait:     claimMaxWait,
	}
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Claim struct {
	RunID            uuid.UUID
	SessionID        uuid.UUID
	Attempt          int32
	Input            string
	Config           json.RawMessage
	Tools            []Tool
	LeaseSeconds     int32
	HeartbeatSeconds int32
}

func (s *Service) Claim(ctx context.Context, workerID string, waitMS int) (Claim, bool, error) {
	if workerID == "" {
		return Claim{}, false, apperr.New(http.StatusBadRequest, "invalid_request", "worker_id")
	}
	if waitMS < 0 {
		return Claim{}, false, apperr.New(http.StatusBadRequest, "invalid_request", "wait_ms")
	}
	wait := time.Duration(waitMS) * time.Millisecond
	if wait > s.claimMaxWait {
		wait = s.claimMaxWait
	}
	deadline := time.Now().Add(wait)
	for {
		row, ok, err := s.claimOnce(ctx, workerID)
		if err != nil {
			return Claim{}, false, err
		}
		if ok {
			return row, true, nil
		}
		// 睡的时候不能占着事务。长事务会把连接和快照一直留到 wait 结束。
		remain := time.Until(deadline)
		if remain <= 0 {
			return Claim{}, false, nil
		}
		step := 500 * time.Millisecond
		if remain < step {
			step = remain
		}
		timer := time.NewTimer(step)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Claim{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

// started 必须和领取在同一个事务里。分开提交的话，状态已经是 running，事件里还没有这次领取。
func (s *Service) claimOnce(ctx context.Context, workerID string) (Claim, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Claim{}, false, err
	}
	defer tx.Rollback(ctx)
	row, err := s.q.WithTx(tx).ClaimRun(ctx, store.ClaimRunParams{
		LeaseOwner:   pgtype.Text{String: workerID, Valid: true},
		LeaseSeconds: s.leaseSeconds,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, false, nil
	}
	if err != nil {
		return Claim{}, false, err
	}
	payload, err := json.Marshal(map[string]any{"attempt": row.Attempt, "worker_id": workerID})
	if err != nil {
		return Claim{}, false, err
	}
	_, last, err := s.events.AppendTx(ctx, tx, row.ID, []events.Event{{
		Type:    "run.started",
		Attempt: row.Attempt,
		Payload: payload,
	}}, &events.Fence{WorkerID: workerID, Attempt: row.Attempt})
	if err != nil {
		return Claim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Claim{}, false, err
	}
	s.events.Publish(row.ID, last)
	claim, err := s.claimFrom(row)
	if err != nil {
		return Claim{}, false, err
	}
	return claim, true, nil
}

type CompleteInput struct {
	RunID    uuid.UUID
	WorkerID string
	Attempt  int32
	Status   string
	Result   json.RawMessage
	Error    *struct {
		Code    string
		Message string
	}
}

func (s *Service) Complete(ctx context.Context, in CompleteInput) error {
	if in.WorkerID == "" {
		return apperr.New(http.StatusBadRequest, "invalid_request", "worker_id")
	}
	if in.Status != "succeeded" && in.Status != "failed" {
		return apperr.New(http.StatusConflict, "invalid_transition", "status")
	}
	var result []byte
	var errCode, errMsg pgtype.Text
	if in.Status == "succeeded" {
		result = []byte(in.Result)
	} else if in.Error != nil {
		errCode = pgtype.Text{String: in.Error.Code, Valid: true}
		errMsg = pgtype.Text{String: in.Error.Message, Valid: true}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// id、attempt、lease_owner、running 四个条件少一个，旧 worker 仍能把 Run 标完成。
	n, err := s.q.WithTx(tx).CompleteRun(ctx, store.CompleteRunParams{
		Status:       in.Status,
		Result:       result,
		ErrorCode:    errCode,
		ErrorMessage: errMsg,
		ID:           in.RunID,
		Attempt:      in.Attempt,
		LeaseOwner:   pgtype.Text{String: in.WorkerID, Valid: true},
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.New(http.StatusConflict, "lease_lost", "租约条件不成立")
	}
	// 完成后 status 不再是 running，fence 会配不上。终态事件跟这次更新同一事务，但不带 fence。
	ev := events.Event{Type: "run.succeeded", Attempt: in.Attempt}
	if in.Status == "succeeded" {
		payload, err := json.Marshal(map[string]any{"result": json.RawMessage(result)})
		if err != nil {
			return err
		}
		if len(result) == 0 {
			payload = []byte(`{"result":null}`)
		}
		ev.Payload = payload
	} else {
		code, msg := "", ""
		if in.Error != nil {
			code, msg = in.Error.Code, in.Error.Message
		}
		payload, err := json.Marshal(map[string]any{"code": code, "message": msg})
		if err != nil {
			return err
		}
		ev.Type = "run.failed"
		ev.Payload = payload
	}
	_, last, err := s.events.AppendTx(ctx, tx, in.RunID, []events.Event{ev}, nil)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.events.Publish(in.RunID, last)
	return nil
}

type ToolOutput struct {
	Status    string
	Output    string
	ErrorCode string
}

func (s *Service) ToolCall(ctx context.Context, runID uuid.UUID, workerID string, attempt int32, toolCallID, name string, args json.RawMessage) (ToolOutput, error) {
	if workerID == "" || toolCallID == "" || name == "" {
		return ToolOutput{}, apperr.New(http.StatusBadRequest, "invalid_request", "tool call")
	}
	// 还没有 tool_executions。事务只把 FOR SHARE 留到检查和执行都做完。
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ToolOutput{}, err
	}
	defer tx.Rollback(ctx)
	row, err := s.q.WithTx(tx).LockRunForTool(ctx, store.LockRunForToolParams{
		ID:         runID,
		Attempt:    attempt,
		LeaseOwner: pgtype.Text{String: workerID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolOutput{}, apperr.New(http.StatusConflict, "lease_lost", "租约条件不成立")
	}
	if err != nil {
		return ToolOutput{}, err
	}
	startedAt := time.Now()
	out := executeTool(row, name, args)
	payloadArgs := args
	if len(payloadArgs) == 0 {
		payloadArgs = []byte("{}")
	}
	started, err := json.Marshal(map[string]any{
		"tool_call_id": toolCallID,
		"name":         name,
		"args":         json.RawMessage(payloadArgs),
	})
	if err != nil {
		return ToolOutput{}, err
	}
	finished, err := json.Marshal(map[string]any{
		"tool_call_id":   toolCallID,
		"status":         out.Status,
		"replayed":       false,
		"exec_count":     1,
		"duration_ms":    time.Since(startedAt).Milliseconds(),
		"output_preview": preview(out.Output),
	})
	if err != nil {
		return ToolOutput{}, err
	}
	// 调用记录要到 D1 才落库。这里还没有重放，exec_count 就是 1。
	_, last, err := s.events.AppendTx(ctx, tx, runID, []events.Event{
		{Type: "tool.started", Attempt: attempt, Payload: started},
		{Type: "tool.finished", Attempt: attempt, Payload: finished},
	}, &events.Fence{WorkerID: workerID, Attempt: attempt})
	if err != nil {
		return ToolOutput{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ToolOutput{}, err
	}
	s.events.Publish(runID, last)
	return out, nil
}

func preview(s string) string {
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200])
	}
	return s
}

func executeTool(config json.RawMessage, name string, args json.RawMessage) ToolOutput {
	var cfg struct {
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return ToolOutput{Status: "failed", Output: "config", ErrorCode: "invalid_args"}
	}
	if !slices.Contains(cfg.Tools, name) {
		return ToolOutput{Status: "failed", Output: "tool not in agent", ErrorCode: "tool_not_in_agent"}
	}
	if _, ok := tools.Get(name); !ok || name != "submit_result" {
		return ToolOutput{Status: "failed", Output: "tool not allowed", ErrorCode: "tool_not_allowed"}
	}
	if err := validateSubmitResult(args); err != nil {
		return ToolOutput{Status: "failed", Output: err.Error(), ErrorCode: "invalid_args"}
	}
	return ToolOutput{Status: "succeeded", Output: "result accepted"}
}

func validateSubmitResult(args json.RawMessage) error {
	if len(args) == 0 {
		return errors.New("summary")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return errors.New("args")
	}
	for key := range obj {
		if key != "summary" && key != "data" {
			return errors.New(key)
		}
	}
	raw, ok := obj["summary"]
	if !ok {
		return errors.New("summary")
	}
	var summary string
	if err := json.Unmarshal(raw, &summary); err != nil {
		return errors.New("summary")
	}
	if n := len([]rune(summary)); n < 1 || n > 4000 {
		return errors.New("summary")
	}
	if raw, ok := obj["data"]; ok {
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			return errors.New("data")
		}
	}
	return nil
}

func (s *Service) claimFrom(row store.ClaimRunRow) (Claim, error) {
	var cfg struct {
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(row.Config, &cfg); err != nil {
		return Claim{}, err
	}
	out := make([]Tool, 0, len(cfg.Tools))
	for _, name := range cfg.Tools {
		spec, ok := tools.Get(name)
		if !ok {
			return Claim{}, fmt.Errorf("tool %s", name)
		}
		out = append(out, Tool{Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters})
	}
	return Claim{
		RunID:            row.ID,
		SessionID:        row.SessionID,
		Attempt:          row.Attempt,
		Input:            row.Input,
		Config:           row.Config,
		Tools:            out,
		LeaseSeconds:     s.leaseSeconds,
		HeartbeatSeconds: s.heartbeatSeconds,
	}, nil
}
