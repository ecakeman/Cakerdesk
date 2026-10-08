package workspace

import (
	"os"
	"path/filepath"
)

func EnsureThread(root, threadID string) (string, error) {
	dir := filepath.Join(root, "threads", threadID)
	for _, name := range []string{"uploads", "work", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}
