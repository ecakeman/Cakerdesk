package repo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	DisplayName  string
}

type Membership struct {
	TenantID   uuid.UUID
	TenantName string
	UserID     uuid.UUID
	Role       string
}

type Session struct {
	UserID    uuid.UUID
	TenantID  uuid.UUID
	Role      string
	ExpiresAt time.Time
}

type APIKey struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Name       string
	Prefix     string
	Role       string
	CreatedBy  *uuid.UUID
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) UserCount(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (r *Repo) Bootstrap(ctx context.Context, email, tenantName, display, passwordHash string) (uuid.UUID, uuid.UUID, error) {
	var tenantID, userID uuid.UUID
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ($1) RETURNING id`, tenantName).Scan(&tenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_quotas (tenant_id) VALUES ($1)`, tenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO users (email, password_hash, display_name) VALUES ($1, $2, $3) RETURNING id`, email, passwordHash, display).Scan(&userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'owner')`, tenantID, userID)
		return err
	})
	return tenantID, userID, err
}

func (r *Repo) UserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `SELECT id, email, password_hash, display_name FROM users WHERE email=$1`, email).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName)
	return u, err
}

func (r *Repo) Membership(ctx context.Context, email, tenantName string) (Membership, error) {
	var m Membership
	err := r.pool.QueryRow(ctx, `
		SELECT t.id, t.name, u.id, ms.role
		FROM users u
		JOIN memberships ms ON ms.user_id = u.id
		JOIN tenants t ON t.id = ms.tenant_id
		WHERE u.email=$1 AND t.name=$2 AND t.status='active'`, email, tenantName).
		Scan(&m.TenantID, &m.TenantName, &m.UserID, &m.Role)
	return m, err
}

func (r *Repo) InsertSession(ctx context.Context, hash []byte, userID, tenantID uuid.UUID, expires time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO console_sessions (id_hash, user_id, tenant_id, expires_at) VALUES ($1, $2, $3, $4)`, hash, userID, tenantID, expires)
	return err
}

func (r *Repo) Session(ctx context.Context, hash []byte) (Session, error) {
	var s Session
	err := r.pool.QueryRow(ctx, `
		SELECT cs.user_id, cs.tenant_id, ms.role, cs.expires_at
		FROM console_sessions cs
		JOIN memberships ms ON ms.user_id = cs.user_id AND ms.tenant_id = cs.tenant_id
		WHERE cs.id_hash=$1`, hash).Scan(&s.UserID, &s.TenantID, &s.Role, &s.ExpiresAt)
	return s, err
}

func (r *Repo) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM console_sessions WHERE id_hash=$1`, hash)
	return err
}

func (r *Repo) InsertAPIKey(ctx context.Context, tenantID uuid.UUID, name, prefix string, keyHash []byte, role string, createdBy uuid.UUID, expires *time.Time) (APIKey, error) {
	var k APIKey
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('cd.tenant_id', $1, true)`, tenantID.String()); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO api_keys (tenant_id, name, prefix, key_hash, role, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, tenant_id, name, prefix, role, created_by, expires_at, revoked_at, last_used_at, created_at`,
			tenantID, name, prefix, keyHash, role, createdBy, expires).
			Scan(&k.ID, &k.TenantID, &k.Name, &k.Prefix, &k.Role, &k.CreatedBy, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedAt)
	})
	return k, err
}

func (r *Repo) ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]APIKey, error) {
	var out []APIKey
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('cd.tenant_id', $1, true)`, tenantID.String()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, name, prefix, role, created_by, expires_at, revoked_at, last_used_at, created_at
			FROM api_keys WHERE tenant_id=$1 ORDER BY created_at DESC, id DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k APIKey
			if err := rows.Scan(&k.ID, &k.TenantID, &k.Name, &k.Prefix, &k.Role, &k.CreatedBy, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedAt); err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repo) APIKeyByPrefix(ctx context.Context, prefix string) (APIKey, []byte, error) {
	var k APIKey
	var hash []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, name, prefix, key_hash, role, created_by, expires_at, revoked_at, last_used_at, created_at
		FROM cd_lookup_api_key($1)`, prefix).
		Scan(&k.ID, &k.TenantID, &k.Name, &k.Prefix, &hash, &k.Role, &k.CreatedBy, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedAt)
	return k, hash, err
}

func (r *Repo) TouchAPIKey(ctx context.Context, id, tenantID uuid.UUID) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('cd.tenant_id', $1, true)`, tenantID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE api_keys SET last_used_at=now()
			WHERE id=$1 AND tenant_id=$2 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id, tenantID)
		return err
	})
}

func (r *Repo) RevokeAPIKey(ctx context.Context, tenantID, id uuid.UUID) (bool, error) {
	var ok bool
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('cd.tenant_id', $1, true)`, tenantID.String()); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND tenant_id=$2 AND revoked_at IS NULL`, id, tenantID)
		if err != nil {
			return err
		}
		ok = tag.RowsAffected() == 1
		if !ok {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE id=$1 AND tenant_id=$2`, id, tenantID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return pgx.ErrNoRows
			}
		}
		return nil
	})
	return ok, err
}

func (r *Repo) Audit(ctx context.Context, tenantID uuid.UUID, actorType string, actorID *uuid.UUID, action, targetType, targetID, ip string) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('cd.tenant_id', $1, true)`, tenantID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_events (tenant_id, actor_type, actor_id, action, target_type, target_id, ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, tenantID, actorType, actorID, action, targetType, targetID, ip)
		return err
	})
}

func (r *Repo) Idempotency(ctx context.Context, tenant uuid.UUID, key string) (hash []byte, status int, body []byte, created time.Time, err error) {
	err = r.pool.QueryRow(ctx, `SELECT request_hash, status_code, response, created_at FROM idempotency_keys WHERE tenant_id=$1 AND key=$2`, tenant, key).
		Scan(&hash, &status, &body, &created)
	return
}

func (r *Repo) SaveIdempotency(ctx context.Context, tenant uuid.UUID, key string, hash []byte, status int, body []byte) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO idempotency_keys (tenant_id, key, request_hash, status_code, response)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT (tenant_id, key) DO NOTHING`, tenant, key, hash, status, body)
	return err
}

func (r *Repo) DeleteIdempotency(ctx context.Context, tenant uuid.UUID, key string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE tenant_id=$1 AND key=$2`, tenant, key)
	return err
}

func IsUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
