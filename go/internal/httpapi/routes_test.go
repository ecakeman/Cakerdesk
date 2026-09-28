package httpapi

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestT_routes_perm(t *testing.T) {
	b, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`\.(GET|POST|PUT|PATCH|DELETE|Any|Handle)\(`)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if call.MatchString(line) && !strings.Contains(line, "requirePerm(") {
			t.Fatalf("line %d missing requirePerm: %s", i+1, line)
		}
	}
}
