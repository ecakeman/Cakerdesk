package repo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Agent struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description string
	Kind        string
	Current     *uuid.UUID
	Status      string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Version struct {
	ID        uuid.UUID
	AgentID   uuid.UUID
	Version   int
	Config    json.RawMessage
	Notes     string
	CreatedAt time.Time
}

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) Create(ctx context.Context, tenant, user uuid.UUID, name, description string, config json.RawMessage, notes, ip string) (Agent, *Version, error) {
	var agent Agent
	var ver *Version
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		var maxAgents, count int
		if err := tx.QueryRow(ctx, `
			SELECT q.max_agents, (SELECT count(*) FROM agents a WHERE a.tenant_id=$1)
			FROM tenant_quotas q WHERE q.tenant_id=$1`, tenant).Scan(&maxAgents, &count); err != nil {
			return err
		}
		if count >= maxAgents {
			return errQuota
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO agents (tenant_id, name, description)
			VALUES ($1, $2, $3)
			RETURNING id, tenant_id, name, description, kind, current_version_id, status, version, created_at, updated_at`,
			tenant, name, description).Scan(scanAgent(&agent)...); err != nil {
			return err
		}
		if len(config) == 0 {
			return nil
		}
		v, err := insertVersion(ctx, tx, tenant, agent.ID, user, 1, config, notes)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			UPDATE agents SET current_version_id=$3, updated_at=now()
			WHERE tenant_id=$1 AND id=$2
			RETURNING id, tenant_id, name, description, kind, current_version_id, status, version, created_at, updated_at`,
			tenant, agent.ID, v.ID).Scan(scanAgent(&agent)...); err != nil {
			return err
		}
		if err := audit(ctx, tx, tenant, user, "agent_version.publish", v.ID.String(), ip); err != nil {
			return err
		}
		ver = &v
		return nil
	})
	return agent, ver, err
}

func (r *Repo) List(ctx context.Context, tenant uuid.UUID, cursorAt *time.Time, cursorID *uuid.UUID, limit int) ([]Agent, error) {
	var out []Agent
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, name, description, kind, current_version_id, status, version, created_at, updated_at
			FROM agents
			WHERE tenant_id=$1
			  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
			ORDER BY created_at DESC, id DESC
			LIMIT $4`, tenant, cursorAt, cursorID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Agent
			if err := rows.Scan(scanAgent(&a)...); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repo) Get(ctx context.Context, tenant, id uuid.UUID) (Agent, error) {
	var a Agent
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT id, tenant_id, name, description, kind, current_version_id, status, version, created_at, updated_at
			FROM agents WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(scanAgent(&a)...)
	})
	return a, err
}

func (r *Repo) Update(ctx context.Context, tenant, id uuid.UUID, name, description *string, expect int) (Agent, error) {
	var a Agent
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE agents SET
				name=COALESCE($3, name),
				description=COALESCE($4, description),
				version=version+1,
				updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND version=$5 AND status='active'`, tenant, id, name, description, expect)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return mismatch(ctx, tx, tenant, id, expect)
		}
		return tx.QueryRow(ctx, `
			SELECT id, tenant_id, name, description, kind, current_version_id, status, version, created_at, updated_at
			FROM agents WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(scanAgent(&a)...)
	})
	return a, err
}

func (r *Repo) Archive(ctx context.Context, tenant, id uuid.UUID, expect int) (Agent, error) {
	var a Agent
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE agents SET status='archived', version=version+1, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND version=$3 AND status='active'`, tenant, id, expect)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return mismatch(ctx, tx, tenant, id, expect)
		}
		return tx.QueryRow(ctx, `
			SELECT id, tenant_id, name, description, kind, current_version_id, status, version, created_at, updated_at
			FROM agents WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(scanAgent(&a)...)
	})
	return a, err
}

func (r *Repo) Publish(ctx context.Context, tenant, agentID, user uuid.UUID, config json.RawMessage, notes, ip string) (Version, error) {
	var v Version
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents WHERE tenant_id=$1 AND id=$2 AND status='active'`, tenant, agentID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return pgx.ErrNoRows
		}
		var next int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0)+1 FROM agent_versions WHERE agent_id=$1`, agentID).Scan(&next); err != nil {
			return err
		}
		created, err := insertVersion(ctx, tx, tenant, agentID, user, next, config, notes)
		if err != nil {
			return err
		}
		v = created
		tag, err := tx.Exec(ctx, `
			UPDATE agents SET current_version_id=$3, version=version+1, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tenant, agentID, v.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return audit(ctx, tx, tenant, user, "agent_version.publish", v.ID.String(), ip)
	})
	return v, err
}

func (r *Repo) Versions(ctx context.Context, tenant, agentID uuid.UUID) ([]Version, error) {
	var out []Version
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents WHERE tenant_id=$1 AND id=$2`, tenant, agentID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return pgx.ErrNoRows
		}
		rows, err := tx.Query(ctx, `
			SELECT id, agent_id, version, config, notes, created_at
			FROM agent_versions WHERE tenant_id=$1 AND agent_id=$2 ORDER BY version ASC`, tenant, agentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Version
			if err := rows.Scan(&v.ID, &v.AgentID, &v.Version, &v.Config, &v.Notes, &v.CreatedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repo) Version(ctx context.Context, tenant, agentID uuid.UUID, number int) (Version, error) {
	var v Version
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT id, agent_id, version, config, notes, created_at
			FROM agent_versions WHERE tenant_id=$1 AND agent_id=$2 AND version=$3`, tenant, agentID, number).
			Scan(&v.ID, &v.AgentID, &v.Version, &v.Config, &v.Notes, &v.CreatedAt)
	})
	return v, err
}

func (r *Repo) SetCurrent(ctx context.Context, tenant, agentID, versionID, user uuid.UUID, ip string) (Agent, error) {
	var a Agent
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setTenant(ctx, tx, tenant); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE agents SET current_version_id=$3, version=version+1, updated_at=now()
			WHERE tenant_id=$1 AND id=$2 AND status='active'
			  AND EXISTS (SELECT 1 FROM agent_versions v WHERE v.tenant_id=$1 AND v.agent_id=$2 AND v.id=$3)`,
			tenant, agentID, versionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		if err := audit(ctx, tx, tenant, user, "agent_version.rollback", versionID.String(), ip); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT id, tenant_id, name, description, kind, current_version_id, status, version, created_at, updated_at
			FROM agents WHERE tenant_id=$1 AND id=$2`, tenant, agentID).Scan(scanAgent(&a)...)
	})
	return a, err
}

func insertVersion(ctx context.Context, tx pgx.Tx, tenant, agentID, user uuid.UUID, number int, config json.RawMessage, notes string) (Version, error) {
	var v Version
	err := tx.QueryRow(ctx, `
		INSERT INTO agent_versions (tenant_id, agent_id, version, config, notes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, agent_id, version, config, notes, created_at`,
		tenant, agentID, number, config, notes, user).
		Scan(&v.ID, &v.AgentID, &v.Version, &v.Config, &v.Notes, &v.CreatedAt)
	return v, err
}

func audit(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID, action, target, ip string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_events (tenant_id, actor_type, actor_id, action, target_type, target_id, ip)
		VALUES ($1, 'user', $2, $3, 'agent_version', $4, $5)`, tenant, user, action, target, ip)
	return err
}

func setTenant(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT set_config('cd.tenant_id', $1, true)`, tenant.String())
	return err
}

func scanAgent(a *Agent) []any {
	return []any{&a.ID, &a.TenantID, &a.Name, &a.Description, &a.Kind, &a.Current, &a.Status, &a.Version, &a.CreatedAt, &a.UpdatedAt}
}

var errQuota = errors.New("quota.max_agents")
var errVersion = errors.New("request.version_mismatch")
var errMissing = errors.New("resource.not_found")

func mismatch(ctx context.Context, tx pgx.Tx, tenant, id uuid.UUID, _ int) error {
	var version int
	var status string
	err := tx.QueryRow(ctx, `SELECT version, status FROM agents WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&version, &status)
	if err == pgx.ErrNoRows {
		return errMissing
	}
	if err != nil {
		return err
	}
	return errVersion
}

func IsQuota(err error) bool   { return errors.Is(err, errQuota) }
func IsVersion(err error) bool { return errors.Is(err, errVersion) }
func IsMissing(err error) bool { return errors.Is(err, errMissing) || errors.Is(err, pgx.ErrNoRows) }
func IsUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
