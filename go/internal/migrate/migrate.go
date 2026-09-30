package migrate

import (
	"database/sql"
	"fmt"

	"github.com/ecakeman/cakerdesk/migrations"
	"github.com/pressly/goose/v3"
)

func init() { // 迁移 SQL 打进二进制，不读磁盘目录。
	goose.SetBaseFS(migrations.FS)
}

func Up(db *sql.DB) error { // 执行尚未应用的 Up。
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

func Down(db *sql.DB) error { // 回滚最近一条。
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Down(db, ".")
}

func Status(db *sql.DB) error { // 打印当前版本。
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Status(db, ".")
}

func Cmd(db *sql.DB, args []string) error { // 解析 up|down|status。
	if len(args) == 0 {
		return fmt.Errorf("migrate up|down|status")
	}
	switch args[0] {
	case "up":
		return Up(db)
	case "down":
		return Down(db)
	case "status":
		return Status(db)
	default:
		return fmt.Errorf("migrate up|down|status")
	}
}
