package archtest

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestImports(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports, Dir: "../.."}
	pkgs, err := packages.Load(cfg, "cakerdesk/internal/...")
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Fatal(pkg.Errors)
		}
		for path := range pkg.Imports {
			if strings.Contains(path, "github.com/docker/docker") && !strings.Contains(pkg.PkgPath, "sandboxd") {
				t.Errorf("%s imports docker client", pkg.PkgPath)
			}
			if !strings.Contains(path, "/repo") {
				continue
			}
			if strings.HasSuffix(pkg.PkgPath, "/app") {
				continue
			}
			mod := moduleName(path)
			if mod == "" {
				t.Errorf("%s imports %s", pkg.PkgPath, path)
				continue
			}
			own := "/internal/" + mod + "/"
			if !strings.Contains(pkg.PkgPath, own+"service") && !strings.Contains(pkg.PkgPath, own+"repo") {
				t.Errorf("%s imports %s", pkg.PkgPath, path)
			}
		}
	}
}

func moduleName(path string) string {
	const mark = "/internal/"
	i := strings.Index(path, mark)
	if i < 0 {
		return ""
	}
	rest := path[i+len(mark):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		return rest[:j]
	}
	return rest
}
