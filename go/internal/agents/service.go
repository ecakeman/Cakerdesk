package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
}

// NewService 包一层 sqlc Queries。写版本时用 pool.Begin 开事务。
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: store.New(pool)}
}

type Agent struct {
	ID             uuid.UUID
	Name           string
	CurrentVersion *int32
}

type Version struct {
	AgentID uuid.UUID
	Version int32
	Config  Config
	Hash    string
}

// Create 插入 agents。名称冲突映射成 409。
func (s *Service) Create(ctx context.Context, name string) (Agent, error) {
	row, err := s.q.CreateAgent(ctx, name)
	if err != nil {
		if isUnique(err) {
			return Agent{}, apperr.New(http.StatusConflict, "agent_exists", "名称已存在")
		}
		return Agent{}, err
	}
	return agentFrom(row), nil
}

// Get 按 id 取 Agent。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Agent, error) {
	row, err := s.q.GetAgent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, apperr.New(http.StatusNotFound, "not_found", "agent 不存在")
	}
	if err != nil {
		return Agent{}, err
	}
	return agentFrom(row), nil
}

// List 按创建时间倒序。
func (s *Service) List(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentFrom(row))
	}
	return out, nil
}

// Publish 锁住 agent 行：hash 相同则不升版本；否则插入下一版并移动指针。
func (s *Service) Publish(ctx context.Context, id uuid.UUID, raw json.RawMessage) (Version, int, error) {
	cfg, hash, err := ParseAndNormalize(raw)
	if err != nil {
		return Version{}, 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Version{}, 0, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	// 锁行，避免两个发布同时读到同一个 max version。
	ag, err := q.LockAgent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, 0, apperr.New(http.StatusNotFound, "not_found", "agent 不存在")
	}
	if err != nil {
		return Version{}, 0, err
	}
	// 与当前指针指向的版本比 hash，相同则 200。
	if ag.CurrentVersion.Valid {
		cur, err := q.GetAgentVersion(ctx, store.GetAgentVersionParams{AgentID: id, Version: ag.CurrentVersion.Int32})
		if err != nil {
			return Version{}, 0, err
		}
		if cur.ConfigHash == hash {
			parsed, err := ParseStored(cur.Config)
			if err != nil {
				return Version{}, 0, err
			}
			if err := tx.Commit(ctx); err != nil {
				return Version{}, 0, err
			}
			return Version{AgentID: id, Version: cur.Version, Config: parsed, Hash: cur.ConfigHash}, http.StatusOK, nil
		}
	}
	// 新版本：max+1，只改 agents.current_version，不改历史行。
	maxv, err := q.MaxAgentVersion(ctx, id)
	if err != nil {
		return Version{}, 0, err
	}
	next := maxv + 1
	row, err := q.InsertAgentVersion(ctx, store.InsertAgentVersionParams{
		AgentID:    id,
		Version:    next,
		Config:     MustJSON(cfg),
		ConfigHash: hash,
	})
	if err != nil {
		return Version{}, 0, err
	}
	if err := q.SetAgentCurrentVersion(ctx, store.SetAgentCurrentVersionParams{
		ID:             id,
		CurrentVersion: pgtype.Int4{Int32: next, Valid: true},
	}); err != nil {
		return Version{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Version{}, 0, err
	}
	return Version{AgentID: id, Version: row.Version, Config: cfg, Hash: row.ConfigHash}, http.StatusCreated, nil
}

// GetVersion 读某一历史版本的规范化 config。
func (s *Service) GetVersion(ctx context.Context, id uuid.UUID, version int32) (Version, error) {
	row, err := s.q.GetAgentVersion(ctx, store.GetAgentVersionParams{AgentID: id, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, apperr.New(http.StatusNotFound, "not_found", "version 不存在")
	}
	if err != nil {
		return Version{}, err
	}
	cfg, err := ParseStored(row.Config)
	if err != nil {
		return Version{}, err
	}
	return Version{AgentID: id, Version: row.Version, Config: cfg, Hash: row.ConfigHash}, nil
}

// agentFrom 把 sqlc 行转成对外结构；空的 current_version 变成 nil。
func agentFrom(row store.Agent) Agent {
	return Agent{ID: row.ID, Name: row.Name, CurrentVersion: int4Ptr(row.CurrentVersion)}
}

// int4Ptr 把 pg 的可空 int 转成 *int32，JSON 才能输出 null。
func int4Ptr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	n := v.Int32
	return &n
}

// isUnique PostgreSQL 唯一约束冲突。
func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
