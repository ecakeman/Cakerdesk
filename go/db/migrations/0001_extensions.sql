-- +goose Up
-- locale 必须是 UTF-8，否则 pg_trgm 会把中文当成非单词字符。
-- +goose StatementBegin
DO $$
DECLARE
  loc text;
BEGIN
  SELECT datctype INTO loc FROM pg_database WHERE datname = current_database();
  IF loc !~* 'utf-?8' THEN
    RAISE EXCEPTION 'lc_ctype must be UTF-8, got %', loc;
  END IF;
END $$;
-- +goose StatementEnd

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS vector;

CREATE SCHEMA IF NOT EXISTS lg;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cd_migrate') THEN
    CREATE ROLE cd_migrate LOGIN PASSWORD 'dev_cd_migrate';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cd_api') THEN
    CREATE ROLE cd_api LOGIN PASSWORD 'dev_cd_api' NOSUPERUSER NOBYPASSRLS;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cd_system') THEN
    CREATE ROLE cd_system LOGIN PASSWORD 'dev_cd_system' BYPASSRLS;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cd_kernel') THEN
    CREATE ROLE cd_kernel LOGIN PASSWORD 'dev_cd_kernel' NOSUPERUSER NOBYPASSRLS;
  END IF;
END $$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA lg TO cd_kernel;
GRANT CREATE ON SCHEMA lg TO cd_kernel;

-- +goose Down
-- 迁移只前进不回退。
SELECT 1;
