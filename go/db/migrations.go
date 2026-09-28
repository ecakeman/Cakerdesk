package db

import "embed"

// Migrations 是 goose 迁移。目录名与规格一致：db/migrations。
//
//go:embed migrations/*.sql
var Migrations embed.FS
