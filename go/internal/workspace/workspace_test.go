package workspace

import "testing"

func TestResolveStaysInsideTheThread(t *testing.T) {
	ok, err := Resolve("/tmp/thread", "artifacts/report.md")
	if err != nil || ok == "" {
		t.Fatal(err)
	}
	for _, rel := range []string{"../secret", "/etc/passwd", "notes.txt", `work\..\secret`} {
		if _, err := Resolve("/tmp/thread", rel); err == nil {
			t.Fatalf("allowed %s", rel)
		}
	}
}
