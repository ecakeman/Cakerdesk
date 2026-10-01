package archtest

import "fmt"

type Rule struct {
	PkgPrefix       string
	ForbiddenImport string
	AllowPrefix     string
}

type Package struct {
	ImportPath string
	Imports    []string
}

func DefaultRules() []Rule {
	// Docker SDK 只能出现在 sandboxd。别的包自己连 docker.sock 会绕开沙箱进程。
	return []Rule{
		{PkgPrefix: "github.com/ecakeman/cakerdesk/internal/", ForbiddenImport: "github.com/docker/", AllowPrefix: "github.com/ecakeman/cakerdesk/internal/sandboxd"},
		{PkgPrefix: "github.com/ecakeman/cakerdesk/internal/", ForbiddenImport: "github.com/moby/", AllowPrefix: "github.com/ecakeman/cakerdesk/internal/sandboxd"},
	}
}

func Check(pkgs []Package, rules []Rule) error {
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

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
