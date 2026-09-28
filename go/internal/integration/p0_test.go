package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cakerdesk/internal/app"
	"cakerdesk/internal/httpapi"
	"cakerdesk/internal/platform/config"
	"cakerdesk/internal/platform/migrate"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var (
	superDSN string
	pool     *pgxpool.Pool
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg16",
		postgres.WithDatabase("cakerdesk"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
	)
	if err != nil {
		panic(err)
	}
	superDSN, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}
	var migrateErr error
	deadline := time.Now().Add(30 * time.Second)
	for {
		migrateErr = migrate.Up(superDSN)
		if migrateErr == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if migrateErr != nil {
		panic(migrateErr)
	}
	pool, err = pgxpool.New(ctx, superDSN)
	if err != nil {
		panic(err)
	}
	code := m.Run()
	pool.Close()
	_ = ctr.Terminate(ctx)
	os.Exit(code)
}

func TestE_auth_login(t *testing.T) {
	ts, srv := newTS(t)
	defer ts.Close()
	if err := srv.Bootstrap(context.Background(), "owner@example.com", "acme", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	bad := postJSON(t, ts.URL+"/v1/auth/login", "", map[string]string{"email": "owner@example.com", "password": "wrong", "tenant": "acme"})
	if bad.StatusCode != 401 || codeOf(t, bad.Body) != "auth.unauthenticated" || !bytes.Contains(bad.Body, []byte("邮箱或密码不正确")) {
		t.Fatalf("bad password: %d %s", bad.StatusCode, bad.Body)
	}
	unknown := postJSON(t, ts.URL+"/v1/auth/login", "", map[string]string{"email": "missing@example.com", "password": "s3cret-password", "tenant": "acme"})
	if unknown.StatusCode != 401 || codeOf(t, unknown.Body) != "auth.unauthenticated" || !bytes.Equal(messageOf(t, bad.Body), messageOf(t, unknown.Body)) {
		t.Fatalf("messages differ: %s vs %s", bad.Body, unknown.Body)
	}
	ok := postJSON(t, ts.URL+"/v1/auth/login", "", map[string]string{"email": "owner@example.com", "password": "s3cret-password", "tenant": "acme"})
	if ok.StatusCode != 200 || ok.Cookie == "" {
		t.Fatalf("login: %d cookie=%q body=%s", ok.StatusCode, ok.Cookie, ok.Body)
	}
	list := get(t, ts.URL+"/v1/agents", ok.Cookie, "")
	if list.StatusCode != 200 {
		t.Fatalf("agents: %d %s", list.StatusCode, list.Body)
	}
}

func TestE_apikey_roundtrip(t *testing.T) {
	ts, _ := newTS(t)
	defer ts.Close()
	login := postJSON(t, ts.URL+"/v1/auth/login", "", map[string]string{"email": "owner@example.com", "password": "s3cret-password", "tenant": "acme"})
	if login.StatusCode != 200 {
		t.Fatal(string(login.Body))
	}
	created := postJSON(t, ts.URL+"/v1/api-keys", login.Cookie, map[string]string{"name": "ci", "role": "developer"})
	if created.StatusCode != 201 {
		t.Fatalf("create key: %d %s", created.StatusCode, created.Body)
	}
	var payload struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(created.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Key) < 12 {
		t.Fatalf("plaintext key missing: %s", created.Body)
	}
	ok := get(t, ts.URL+"/v1/agents", "", payload.Key)
	if ok.StatusCode != 200 {
		t.Fatalf("use key: %d %s", ok.StatusCode, ok.Body)
	}
	rev := del(t, ts.URL+"/v1/api-keys/"+payload.ID, login.Cookie)
	if rev.StatusCode != 200 {
		t.Fatalf("revoke: %d %s", rev.StatusCode, rev.Body)
	}
	denied := get(t, ts.URL+"/v1/agents", "", payload.Key)
	if denied.StatusCode != 401 || codeOf(t, denied.Body) != "auth.key_revoked" {
		t.Fatalf("revoked key: %d %s", denied.StatusCode, denied.Body)
	}
}

func TestT_rls(t *testing.T) {
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	seedTenant(t, ctx, a, "rls-a")
	seedTenant(t, ctx, b, "rls-b")
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("cd_api", "dev_cd_api")
	apiPool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer apiPool.Close()
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname='public' AND c.relkind='r' AND c.relrowsecurity
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) == 0 {
		t.Fatal("no RLS tables")
	}
	conn, err := apiPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('cd.tenant_id', $1, true)`, a.String()); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+quoteIdent(table)).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 1 {
			t.Fatalf("%s visible rows = %d, want 1", table, n)
		}
	}
}

func newTS(t *testing.T) (*httptest.Server, *httpapi.Server) {
	t.Helper()
	cfg := config.Config{LLMAliases: []string{"gpt-4.5"}, SecureCookie: true, InternalToken: "token", MasterKey: "master"}
	srv := app.New(cfg, pool)
	return httptest.NewServer(srv.Public), srv
}

func seedTenant(t *testing.T, ctx context.Context, tenant uuid.UUID, name string) {
	t.Helper()
	agent := uuid.New()
	ver := uuid.New()
	skill := uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenant, name); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agents (id, tenant_id, name) VALUES ($1, $2, $3)`, agent, tenant, name+"-agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_versions (id, tenant_id, agent_id, version, config) VALUES ($1, $2, $3, 1, '{}')`, ver, tenant, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET current_version_id=$1 WHERE id=$2`, ver, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO skills (id, tenant_id, name) VALUES ($1, $2, $3)`, skill, tenant, name+"-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO skill_versions (tenant_id, skill_id, tree_hash, semver, description, front_matter, status, size_bytes, file_count)
		VALUES ($1, $2, $3, '0.1.0', '01234567890123456789', '{"name":"csv-cleaning"}', 'published', 1, 1)`,
		tenant, skill, "sha256:"+name); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO secrets (tenant_id, name, ciphertext, nonce, key_version) VALUES ($1, 'EXAMPLE_TOKEN', '\x00', '\x000102030405060708090a0b', 1)`, tenant); err != nil {
		t.Fatal(err)
	}
	prefix := strings.ReplaceAll(tenant.String(), "-", "")[:12]
	if _, err := tx.Exec(ctx, `INSERT INTO api_keys (tenant_id, name, prefix, key_hash, role) VALUES ($1, 'k', $2, $3, 'viewer')`, tenant, prefix, []byte(tenant.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (tenant_id, actor_type, action, target_type) VALUES ($1, 'user', 'seed', 'tenant')`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func quoteIdent(name string) string {
	return `"` + name + `"`
}

type reply struct {
	StatusCode int
	Body       []byte
	Cookie     string
}

func postJSON(t *testing.T, url, cookie string, body any) reply {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "cd_session", Value: cookie})
	}
	return do(t, req)
}

func get(t *testing.T, url, cookie, bearer string) reply {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "cd_session", Value: cookie})
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return do(t, req)
}

func del(t *testing.T, url, cookie string) reply {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "cd_session", Value: cookie})
	return do(t, req)
}

func do(t *testing.T, req *http.Request) reply {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	out := reply{StatusCode: res.StatusCode, Body: body}
	for _, c := range res.Cookies() {
		if c.Name == "cd_session" {
			out.Cookie = c.Value
		}
	}
	return out
}

func codeOf(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Error.Code
}

func messageOf(t *testing.T, body []byte) []byte {
	t.Helper()
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	return []byte(payload.Error.Message)
}
