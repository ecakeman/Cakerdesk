package service

import (
	"context"

	"cakerdesk/internal/secrets/repo"

	"github.com/google/uuid"
)

type Service struct{ repo *repo.Repo }

func New(r *repo.Repo) *Service { return &Service{repo: r} }

func (s *Service) Exists(ctx context.Context, tenant uuid.UUID, name string) (bool, error) {
	return s.repo.Exists(ctx, tenant, name)
}
