-- +goose Up
CREATE TABLE skills (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    UNIQUE (tenant_id, id)
);

CREATE TABLE skill_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    tree_hash text NOT NULL,
    semver text NOT NULL,
    description text NOT NULL,
    allowed_tools text[],
    front_matter jsonb NOT NULL,
    status text NOT NULL CHECK (status IN ('uploaded', 'scanning', 'reviewing', 'review_failed', 'published', 'rejected')),
    scan_findings jsonb NOT NULL DEFAULT '[]',
    review_findings jsonb NOT NULL DEFAULT '{}',
    review_run_id uuid,
    size_bytes bigint NOT NULL,
    file_count int NOT NULL,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    UNIQUE (tenant_id, tree_hash),
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, skill_id) REFERENCES skills(tenant_id, id) ON DELETE CASCADE
);

GRANT USAGE ON SCHEMA public TO cd_api, cd_system, cd_kernel, cd_migrate;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO cd_api, cd_system;

-- API Key 查找发生在得知 tenant 之前，不能靠 SET LOCAL 通过 RLS。
-- 函数属主是迁移账号（超级用户），只按 prefix 返回一行。
-- +goose StatementBegin
CREATE FUNCTION cd_lookup_api_key(p_prefix text)
RETURNS SETOF api_keys
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
  SELECT * FROM api_keys WHERE prefix = p_prefix
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION cd_lookup_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION cd_lookup_api_key(text) TO cd_api, cd_system;

-- 本阶段已存在且规格要求启用 RLS 的表。
-- +goose StatementBegin
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'agents', 'agent_versions', 'skills', 'skill_versions', 'secrets', 'api_keys', 'audit_events'
  ]
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''cd.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''cd.tenant_id'', true)::uuid)',
      t
    );
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
