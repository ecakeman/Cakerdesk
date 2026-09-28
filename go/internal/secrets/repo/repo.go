package repo

import (
	"context"

	"cakerdesk/internal/platform/dbx"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) Exists(ctx context.Context, tenant uuid.UUID, name string) (bool, error) {
	var ok bool
	err := dbx.WithTenant(ctx, r.pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM secrets WHERE tenant_id=$1 AND name=$2)`, tenant, name).Scan(&ok)
	})
	return ok, err
}
