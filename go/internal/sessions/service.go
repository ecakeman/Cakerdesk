package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ecakeman/cakerdesk/internal/agents"
	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/events"
	"github.com/ecakeman/cakerdesk/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool       *pgxpool.Pool
	q          *store.Queries
	workspaces string
	events     *events.Service
}

func NewService(pool *pgxpool.Pool, workspaces string, ev *events.Service) *Service {
	return &Service{pool: pool, q: store.New(pool), workspaces: workspaces, events: ev}
}

type Session struct {
	ID          uuid.UUID
	AgentID     uuid.UUID
	Title       string
	CreatedAt   time.Time
	ActiveRunID *uuid.UUID
}

type Run struct {
	ID           uuid.UUID
	SessionID    uuid.UUID
	AgentVersion int32
	Status       string
	Attempt      int32
	Result       json.RawMessage
	ErrorCode    *string
	ErrorMessage *string
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	Config       json.RawMessage
}

func (s *Service) Create(ctx context.Context, agentID uuid.UUID, title string) (Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	ag, err := q.GetAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, apperr.New(http.StatusNotFound, "not_found", "agent 不存在")
	}
	if err != nil {
		return Session{}, err
	}
	if !ag.CurrentVersion.Valid {
		return Session{}, apperr.New(http.StatusConflict, "agent_unpublished", "agent 还没有版本")
	}
	row, err := q.InsertSession(ctx, store.InsertSessionParams{AgentID: agentID, Title: title})
	if err != nil {
		return Session{}, err
	}
	// 目录失败必须回滚插入。先提交再 mkdir，库里会留下一个没有工作区的 Session。
	dir := filepath.Join(s.workspaces, row.ID.String())
	if err := os.MkdirAll(s.workspaces, 0o755); err != nil {
		return Session{}, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return Session{}, err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		os.Remove(dir)
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		os.Remove(dir)
		return Session{}, err
	}
	return Session{ID: row.ID, AgentID: row.AgentID, Title: row.Title, CreatedAt: row.CreatedAt.Time}, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Session, error) {
	row, err := s.q.GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, apperr.New(http.StatusNotFound, "not_found", "session 不存在")
	}
	if err != nil {
		return Session{}, err
	}
	out := Session{ID: row.ID, AgentID: row.AgentID, Title: row.Title, CreatedAt: row.CreatedAt.Time}
	active, err := s.q.ActiveRunID(ctx, id)
	if err == nil {
		out.ActiveRunID = &active
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Session{}, err
	}
	return out, nil
}

func (s *Service) CreateRun(ctx context.Context, sessionID uuid.UUID, input string) (Run, error) {
	if n := len([]rune(input)); n < 1 || n > 20000 {
		return Run{}, apperr.New(http.StatusBadRequest, "invalid_request", "input")
	}
	sess, err := s.q.GetSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, apperr.New(http.StatusNotFound, "not_found", "session 不存在")
	}
	if err != nil {
		return Run{}, err
	}
	ver, err := s.q.GetCurrentAgentVersion(ctx, sess.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, apperr.New(http.StatusConflict, "agent_unpublished", "agent 还没有版本")
	}
	if err != nil {
		return Run{}, err
	}
	cfg, err := agents.ParseStored(ver.Config)
	if err != nil {
		return Run{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	// 不先查再插。两个请求会同时看到「没有活跃 Run」。唯一索引冲突才表示忙。
	row, err := s.q.WithTx(tx).InsertRun(ctx, store.InsertRunParams{
		SessionID:    sessionID,
		AgentID:      sess.AgentID,
		AgentVersion: ver.Version,
		Config:       ver.Config,
		Input:        input,
		MaxAttempts:  int32(cfg.Limits.MaxAttempts),
	})
	if err != nil {
		if isActiveConflict(err) {
			return Run{}, s.busy(ctx, sessionID)
		}
		return Run{}, err
	}
	payload, err := json.Marshal(map[string]any{"agent_version": ver.Version})
	if err != nil {
		return Run{}, err
	}
	_, last, err := s.events.AppendTx(ctx, tx, row.ID, []events.Event{{
		Type:    "run.queued",
		Attempt: row.Attempt,
		Payload: payload,
	}}, nil)
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	s.events.Publish(row.ID, last)
	return runFromInsert(row), nil
}

func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	row, err := s.q.GetRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, apperr.New(http.StatusNotFound, "not_found", "run 不存在")
	}
	if err != nil {
		return Run{}, err
	}
	return runFromGet(row), nil
}

func (s *Service) ListRuns(ctx context.Context, sessionID uuid.UUID) ([]Run, error) {
	if _, err := s.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListRunsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, runFromList(row))
	}
	return out, nil
}

func (s *Service) busy(ctx context.Context, sessionID uuid.UUID) error {
	id, err := s.q.ActiveRunID(ctx, sessionID)
	if err != nil {
		return apperr.New(http.StatusConflict, "session_busy", "已有活跃 Run")
	}
	return apperr.New(http.StatusConflict, "session_busy", "已有活跃 Run "+id.String())
}

func isActiveConflict(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "runs_one_active_per_session"
}

func runFromInsert(row store.InsertRunRow) Run {
	return fillRun(row.ID, row.SessionID, row.AgentVersion, row.Status, row.Attempt, row.Result, row.ErrorCode, row.ErrorMessage, row.CreatedAt, row.StartedAt, row.FinishedAt, row.Config)
}

func runFromGet(row store.GetRunRow) Run {
	return fillRun(row.ID, row.SessionID, row.AgentVersion, row.Status, row.Attempt, row.Result, row.ErrorCode, row.ErrorMessage, row.CreatedAt, row.StartedAt, row.FinishedAt, row.Config)
}

func runFromList(row store.ListRunsBySessionRow) Run {
	return fillRun(row.ID, row.SessionID, row.AgentVersion, row.Status, row.Attempt, row.Result, row.ErrorCode, row.ErrorMessage, row.CreatedAt, row.StartedAt, row.FinishedAt, row.Config)
}

func fillRun(id, sessionID uuid.UUID, version int32, status string, attempt int32, result []byte, errCode, errMsg pgtype.Text, created pgtype.Timestamptz, started, finished pgtype.Timestamptz, config json.RawMessage) Run {
	run := Run{
		ID:           id,
		SessionID:    sessionID,
		AgentVersion: version,
		Status:       status,
		Attempt:      attempt,
		CreatedAt:    created.Time,
		Config:       config,
	}
	if len(result) > 0 {
		run.Result = json.RawMessage(result)
	}
	if errCode.Valid {
		run.ErrorCode = &errCode.String
	}
	if errMsg.Valid {
		run.ErrorMessage = &errMsg.String
	}
	if started.Valid {
		t := started.Time
		run.StartedAt = &t
	}
	if finished.Valid {
		t := finished.Time
		run.FinishedAt = &t
	}
	return run
}
