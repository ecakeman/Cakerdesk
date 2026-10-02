package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/store"
	"github.com/ecakeman/cakerdesk/internal/tools"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	q                *store.Queries
	leaseSeconds     int32
	heartbeatSeconds int32
	claimMaxWait     time.Duration
}

func NewService(pool *pgxpool.Pool, leaseSeconds, heartbeatSeconds int, claimMaxWait time.Duration) *Service {
	return &Service{
		q:                store.New(pool),
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
		row, err := s.q.ClaimRun(ctx, store.ClaimRunParams{
			LeaseOwner:   pgtype.Text{String: workerID, Valid: true},
			LeaseSeconds: s.leaseSeconds,
		})
		if err == nil {
			claim, err := s.claimFrom(row)
			if err != nil {
				return Claim{}, false, err
			}
			return claim, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Claim{}, false, err
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
	// id、attempt、lease_owner、running 四个条件少一个，旧 worker 仍能把 Run 标完成。
	n, err := s.q.CompleteRun(ctx, store.CompleteRunParams{
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
