package archtest

import "fmt"

// Rule 用包路径前缀判断哪些 import 不允许。
type Rule struct {
	PkgPrefix       string
	ForbiddenImport string
	AllowPrefix     string
}

type Package struct {
	ImportPath string
	Imports    []string
}

func DefaultRules() []Rule { // A1：仅 sandboxd 可依赖 Docker SDK。
	return []Rule{
		{PkgPrefix: "github.com/ecakeman/cakerdesk/internal/", ForbiddenImport: "github.com/docker/", AllowPrefix: "github.com/ecakeman/cakerdesk/internal/sandboxd"},
		{PkgPrefix: "github.com/ecakeman/cakerdesk/internal/", ForbiddenImport: "github.com/moby/", AllowPrefix: "github.com/ecakeman/cakerdesk/internal/sandboxd"},
	}
}

func Check(pkgs []Package, rules []Rule) error { // 真实依赖图 + 构造列表都能走这里。
	for _, pkg := range pkgs {
		for _, imp := range pkg.Imports {
			for _, rule := range rules {
				if !hasPrefix(pkg.ImportPath, rule.PkgPrefix) {
					continue
				}
				if rule.AllowPrefix != "" && hasPrefix(pkg.ImportPath, rule.AllowPrefix) {
					continue
				}
				if hasPrefix(imp, rule.ForbiddenImport) {
					return fmt.Errorf("%s imports %s", pkg.ImportPath, imp)
				}
			}
		}
	}
	return nil
}

func hasPrefix(s, prefix string) bool { // 避免再引 strings 包。
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
