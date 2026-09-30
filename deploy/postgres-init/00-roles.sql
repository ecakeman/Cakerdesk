-- 超级用户只在这里跑一次。之后业务库属主是 cd_migrate。
CREATE ROLE cd_migrate LOGIN PASSWORD 'cd_migrate';
CREATE ROLE cd_app LOGIN PASSWORD 'cd_app';
CREATE ROLE cd_kernel LOGIN PASSWORD 'cd_kernel';
ALTER ROLE cd_kernel SET search_path = lg;

CREATE DATABASE cakerdesk OWNER cd_migrate;
GRANT CONNECT ON DATABASE cakerdesk TO cd_app;
GRANT CONNECT ON DATABASE cakerdesk TO cd_kernel;
