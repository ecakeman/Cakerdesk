package service

import (
	"context"

	"cakerdesk/internal/skills/repo"

	"github.com/google/uuid"
)

type Service struct{ repo *repo.Repo }

func New(r *repo.Repo) *Service { return &Service{repo: r} }

func (s *Service) PublishedByHash(ctx context.Context, tenant uuid.UUID, hash string) (repo.Published, error) {
	return s.repo.PublishedByHash(ctx, tenant, hash)
}
