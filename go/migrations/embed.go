package migrations

import "embed"

//go:embed *.sql
var FS embed.FS // goose 从这里读迁移，不依赖仓库路径。
