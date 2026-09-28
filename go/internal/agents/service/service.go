package service

import (
	"context"
	"encoding/json"
	"time"

	"cakerdesk/internal/agents/repo"
	"cakerdesk/internal/platform/apperr"
	secretssvc "cakerdesk/internal/secrets/service"
	skillssvc "cakerdesk/internal/skills/service"

	"github.com/google/uuid"
)

type Agent = repo.Agent
type Version = repo.Version

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Service struct {
	repo    *repo.Repo
	skills  *skillssvc.Service
	secrets *secretssvc.Service
	aliases []string
}

func New(r *repo.Repo, skills *skillssvc.Service, secrets *secretssvc.Service, aliases []string) *Service {
	return &Service{repo: r, skills: skills, secrets: secrets, aliases: aliases}
}

func (s *Service) check(ctx context.Context, tenant uuid.UUID, raw json.RawMessage) error {
	return Validate(raw, s.aliases, func(hash string) (SkillHit, error) {
		hit, err := s.skills.PublishedByHash(ctx, tenant, hash)
		if err != nil {
			return SkillHit{}, err
		}
		return SkillHit{FrontName: hit.FrontName, Allowed: hit.Allowed, Declared: hit.Declared, Found: hit.Found}, nil
	}, func(name string) (bool, error) {
		return s.secrets.Exists(ctx, tenant, name)
	})
}

func (s *Service) Create(ctx context.Context, tenant, user uuid.UUID, name, description string, config json.RawMessage, ip string) (repo.Agent, *repo.Version, error) {
	if name == "" {
		return repo.Agent{}, nil, apperr.Invalid("name 不能为空")
	}
	if len(config) > 0 {
		if err := s.check(ctx, tenant, config); err != nil {
			return repo.Agent{}, nil, err
		}
	}
	agent, ver, err := s.repo.Create(ctx, tenant, user, name, description, config, "", ip)
	if repo.IsQuota(err) {
		return repo.Agent{}, nil, apperr.Conflict("quota.max_agents", "Agent 数量已达配额")
	}
	if repo.IsUnique(err) {
		return repo.Agent{}, nil, apperr.New(422, "request.validation_failed", "同一租户内 Agent 名称已存在")
	}
	return agent, ver, err
}

func (s *Service) List(ctx context.Context, tenant uuid.UUID, cursorAt *time.Time, cursorID *uuid.UUID, limit int) ([]repo.Agent, error) {
	return s.repo.List(ctx, tenant, cursorAt, cursorID, limit)
}

func (s *Service) Get(ctx context.Context, tenant, id uuid.UUID) (repo.Agent, error) {
	a, err := s.repo.Get(ctx, tenant, id)
	if repo.IsMissing(err) {
		return repo.Agent{}, apperr.NotFound()
	}
	return a, err
}

func (s *Service) Update(ctx context.Context, tenant, id uuid.UUID, name, description *string, expect int) (repo.Agent, error) {
	a, err := s.repo.Update(ctx, tenant, id, name, description, expect)
	return a, mapAgentErr(err)
}

func (s *Service) Archive(ctx context.Context, tenant, id uuid.UUID, expect int) (repo.Agent, error) {
	a, err := s.repo.Archive(ctx, tenant, id, expect)
	return a, mapAgentErr(err)
}

func (s *Service) Publish(ctx context.Context, tenant, agentID, user uuid.UUID, config json.RawMessage, notes, ip string) (repo.Version, []Warning, error) {
	if err := s.check(ctx, tenant, config); err != nil {
		return repo.Version{}, nil, err
	}
	v, err := s.repo.Publish(ctx, tenant, agentID, user, config, notes, ip)
	if err != nil {
		return repo.Version{}, nil, mapAgentErr(err)
	}
	return v, []Warning{}, nil
}

func (s *Service) Versions(ctx context.Context, tenant, agentID uuid.UUID) ([]repo.Version, error) {
	items, err := s.repo.Versions(ctx, tenant, agentID)
	if repo.IsMissing(err) {
		return nil, apperr.NotFound()
	}
	return items, err
}

func (s *Service) Version(ctx context.Context, tenant, agentID uuid.UUID, number int) (repo.Version, error) {
	v, err := s.repo.Version(ctx, tenant, agentID, number)
	if repo.IsMissing(err) {
		return repo.Version{}, apperr.NotFound()
	}
	return v, err
}

func (s *Service) SetCurrent(ctx context.Context, tenant, agentID, versionID, user uuid.UUID, ip string) (repo.Agent, error) {
	a, err := s.repo.SetCurrent(ctx, tenant, agentID, versionID, user, ip)
	return a, mapAgentErr(err)
}

func mapAgentErr(err error) error {
	switch {
	case err == nil:
		return nil
	case repo.IsMissing(err):
		return apperr.NotFound()
	case repo.IsVersion(err):
		return apperr.VersionMismatch()
	default:
		return err
	}
}
