package workspace

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func Resolve(root, rel string) (string, error) {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || strings.Contains(rel, "\\") || strings.Contains(rel, "\x00") {
		return "", fmt.Errorf("invalid path")
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("invalid path")
	}
	parts := strings.Split(cleaned, "/")
	switch parts[0] {
	case "uploads", "work", "artifacts":
	default:
		return "", fmt.Errorf("invalid path")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(cleaned))
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	relOut, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil || relOut == ".." || strings.HasPrefix(relOut, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid path")
	}
	return fullAbs, nil
}

func EnsureThread(root, threadID string) (string, error) {
	dir := filepath.Join(root, "threads", threadID)
	for _, name := range []string{"uploads", "work", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}
