package repo

import (
	"context"

	"cakerdesk/internal/platform/dbx"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Published struct {
	FrontName string
	Allowed   []string
	Declared  bool
	Found     bool
}

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) PublishedByHash(ctx context.Context, tenant uuid.UUID, hash string) (Published, error) {
	var out Published
	err := dbx.WithTenant(ctx, r.pool, tenant, func(tx pgx.Tx) error {
		var allowed []string
		err := tx.QueryRow(ctx, `
			SELECT front_matter->>'name', allowed_tools IS NOT NULL, allowed_tools
			FROM skill_versions
			WHERE tenant_id=$1 AND tree_hash=$2 AND status='published'`, tenant, hash).
			Scan(&out.FrontName, &out.Declared, &allowed)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		out.Allowed = allowed
		out.Found = true
		return nil
	})
	return out, err
}
