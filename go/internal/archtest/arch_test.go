package archtest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArchRules(t *testing.T) {
	pkgs := loadModuleDeps(t)
	if err := Check(pkgs, DefaultRules()); err != nil {
		t.Fatal(err)
	}
	bad := []Package{{
		ImportPath: "github.com/ecakeman/cakerdesk/internal/foo",
		Imports:    []string{"github.com/docker/docker/client"},
	}}
	if err := Check(bad, DefaultRules()); err == nil {
		t.Fatal("构造的违规依赖没有被检出")
	}
	allowed := []Package{{
		ImportPath: "github.com/ecakeman/cakerdesk/internal/sandboxd",
		Imports:    []string{"github.com/moby/moby/client"},
	}}
	if err := Check(allowed, DefaultRules()); err != nil {
		t.Fatal(err)
	}
}

func loadModuleDeps(t *testing.T) []Package {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-json", "./...")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var pkgs []Package
	for dec.More() {
		var row struct {
			ImportPath string
			Imports    []string
		}
		if err := dec.Decode(&row); err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, Package{ImportPath: row.ImportPath, Imports: row.Imports})
	}
	return pkgs
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
