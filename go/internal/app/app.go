package app

import (
	agentsrepo "cakerdesk/internal/agents/repo"
	agentsvc "cakerdesk/internal/agents/service"
	authrepo "cakerdesk/internal/auth/repo"
	authsvc "cakerdesk/internal/auth/service"
	"cakerdesk/internal/httpapi"
	"cakerdesk/internal/platform/config"
	secretsrepo "cakerdesk/internal/secrets/repo"
	secretssvc "cakerdesk/internal/secrets/service"
	skillsrepo "cakerdesk/internal/skills/repo"
	skillssvc "cakerdesk/internal/skills/service"

	"github.com/jackc/pgx/v5/pgxpool"
)

func New(cfg config.Config, pool *pgxpool.Pool) *httpapi.Server {
	auth := authsvc.New(authrepo.New(pool))
	agents := agentsvc.New(agentsrepo.New(pool), skillssvc.New(skillsrepo.New(pool)), secretssvc.New(secretsrepo.New(pool)), cfg.LLMAliases)
	return httpapi.New(pool, auth, agents, cfg.SecureCookie)
}
