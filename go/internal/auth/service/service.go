package service

import (
	"context"
	"strings"
	"time"

	"cakerdesk/internal/auth/repo"
	"cakerdesk/internal/platform/apperr"
	"cakerdesk/internal/platform/crypto"
	"cakerdesk/internal/platform/ids"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	sessionTTL = 7 * 24 * time.Hour
	loginFail  = "邮箱或密码不正确"
)

type APIKey = repo.APIKey

type Actor struct {
	UserID    uuid.UUID
	TenantID  uuid.UUID
	Role      string
	Session   []byte
	APIKeyID  *uuid.UUID
	KeyPrefix string
}

type CreatedKey struct {
	Key    repo.APIKey
	Secret string
}

type Service struct {
	repo *repo.Repo
}

func New(r *repo.Repo) *Service { return &Service{repo: r} }

func (s *Service) Bootstrap(ctx context.Context, email, tenantName, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	tenantName = strings.TrimSpace(tenantName)
	if email == "" || tenantName == "" || password == "" {
		return apperr.Invalid("email、tenant、password 都不能为空")
	}
	n, err := s.repo.UserCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperr.Conflict("request.validation_failed", "已经完成 bootstrap")
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	display := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		display = email[:i]
	}
	_, _, err = s.repo.Bootstrap(ctx, email, tenantName, display, hash)
	if repo.IsUnique(err) {
		return apperr.Conflict("request.validation_failed", "租户或邮箱已存在")
	}
	return err
}

type LoginResult struct {
	Token    string
	UserID   uuid.UUID
	TenantID uuid.UUID
	Role     string
	Email    string
}

func (s *Service) Login(ctx context.Context, email, tenantName, password string) (LoginResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	tenantName = strings.TrimSpace(tenantName)
	user, err := s.repo.UserByEmail(ctx, email)
	if err != nil || !crypto.VerifyPassword(user.PasswordHash, password) {
		return LoginResult{}, apperr.Unauth(loginFail)
	}
	m, err := s.repo.Membership(ctx, email, tenantName)
	if err != nil {
		return LoginResult{}, apperr.Unauth(loginFail)
	}
	token, err := crypto.Base62(43)
	if err != nil {
		return LoginResult{}, err
	}
	sum := crypto.SHA256([]byte(token))
	expires := time.Now().Add(sessionTTL)
	if err := s.repo.InsertSession(ctx, sum, m.UserID, m.TenantID, expires); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, UserID: m.UserID, TenantID: m.TenantID, Role: m.Role, Email: email}, nil
}

func (s *Service) Logout(ctx context.Context, sessionHash []byte) error {
	return s.repo.DeleteSession(ctx, sessionHash)
}

func (s *Service) ActorFromSession(ctx context.Context, token string) (Actor, error) {
	sum := crypto.SHA256([]byte(token))
	sess, err := s.repo.Session(ctx, sum)
	if err != nil {
		return Actor{}, apperr.Unauth("登录已失效")
	}
	if time.Now().After(sess.ExpiresAt) {
		return Actor{}, apperr.Unauth("登录已失效")
	}
	return Actor{UserID: sess.UserID, TenantID: sess.TenantID, Role: sess.Role, Session: sum}, nil
}

func (s *Service) ActorFromAPIKey(ctx context.Context, raw string) (Actor, error) {
	if !strings.HasPrefix(raw, "cdk_live_") || len(raw) < 12 {
		return Actor{}, apperr.Unauth("缺少凭证")
	}
	key, hash, err := s.repo.APIKeyByPrefix(ctx, raw[:12])
	if err != nil {
		return Actor{}, apperr.Unauth("缺少凭证")
	}
	if !bytesEqual(hash, crypto.SHA256([]byte(raw))) {
		return Actor{}, apperr.Unauth("缺少凭证")
	}
	if key.RevokedAt != nil {
		return Actor{}, apperr.New(401, "auth.key_revoked", "API Key 已吊销")
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return Actor{}, apperr.New(401, "auth.key_expired", "API Key 已过期")
	}
	_ = s.repo.TouchAPIKey(ctx, key.ID, key.TenantID)
	id := key.ID
	return Actor{UserID: deref(key.CreatedBy), TenantID: key.TenantID, Role: key.Role, APIKeyID: &id, KeyPrefix: key.Prefix}, nil
}

func (s *Service) CreateAPIKey(ctx context.Context, actor Actor, name, role string, expires *time.Time, ip string) (CreatedKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CreatedKey{}, apperr.Invalid("name 不能为空")
	}
	want, ok := RoleRank(role)
	if !ok {
		return CreatedKey{}, apperr.Invalid("role 不合法")
	}
	have, ok := RoleRank(actor.Role)
	if !ok || want > have {
		return CreatedKey{}, apperr.Forbidden()
	}
	body, err := crypto.Base62(32)
	if err != nil {
		return CreatedKey{}, err
	}
	secret := "cdk_live_" + body
	var created repo.APIKey
	for i := 0; i < 3; i++ {
		created, err = s.repo.InsertAPIKey(ctx, actor.TenantID, name, secret[:12], crypto.SHA256([]byte(secret)), role, actor.UserID, expires)
		if err == nil {
			break
		}
		if !repo.IsUnique(err) {
			return CreatedKey{}, err
		}
		body, err = crypto.Base62(32)
		if err != nil {
			return CreatedKey{}, err
		}
		secret = "cdk_live_" + body
	}
	if err != nil {
		return CreatedKey{}, err
	}
	actorID := actor.UserID
	_ = s.repo.Audit(ctx, actor.TenantID, actorType(actor), &actorID, "api_key.create", "api_key", ids.Format(ids.APIKey, created.ID), ip)
	return CreatedKey{Key: created, Secret: secret}, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, actor Actor) ([]repo.APIKey, error) {
	return s.repo.ListAPIKeys(ctx, actor.TenantID)
}

func (s *Service) RevokeAPIKey(ctx context.Context, actor Actor, id uuid.UUID, ip string) error {
	ok, err := s.repo.RevokeAPIKey(ctx, actor.TenantID, id)
	if err != nil {
		if repo.IsNoRows(err) || err == pgx.ErrNoRows {
			return apperr.NotFound()
		}
		return err
	}
	if !ok {
		return nil
	}
	actorID := actor.UserID
	_ = s.repo.Audit(ctx, actor.TenantID, actorType(actor), &actorID, "api_key.revoke", "api_key", ids.Format(ids.APIKey, id), ip)
	return nil
}

func (s *Service) ReadIdempotency(ctx context.Context, tenant uuid.UUID, key string, hash []byte) (status int, body []byte, hit bool, err error) {
	stored, code, resp, created, err := s.repo.Idempotency(ctx, tenant, key)
	if repo.IsNoRows(err) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	if time.Since(created) > 24*time.Hour {
		_ = s.repo.DeleteIdempotency(ctx, tenant, key)
		return 0, nil, false, nil
	}
	if !bytesEqual(stored, hash) {
		return 0, nil, false, apperr.New(422, "idempotency.mismatch", "相同 Idempotency-Key 的请求体不一致")
	}
	return code, resp, true, nil
}

func (s *Service) SaveIdempotency(ctx context.Context, tenant uuid.UUID, key string, hash []byte, status int, body []byte) error {
	return s.repo.SaveIdempotency(ctx, tenant, key, hash, status, body)
}

func actorType(a Actor) string {
	if a.APIKeyID != nil {
		return "api_key"
	}
	return "user"
}

func deref(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
