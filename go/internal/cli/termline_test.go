package cli

import "testing"

func TestBackspaceRemovesOneWideRune(t *testing.T) {
	var line lineEdit
	if line.push('你') != 2 || line.push(' ') != 1 || line.push('好') != 2 {
		t.Fatal(line.String())
	}
	if cols := line.backspace(); cols != 2 || line.String() != "你 " {
		t.Fatalf("after 好: %d %q", cols, line.String())
	}
	if cols := line.backspace(); cols != 1 || line.String() != "你" {
		t.Fatalf("after space: %d %q", cols, line.String())
	}
	if cols := line.backspace(); cols != 2 || line.String() != "" {
		t.Fatalf("after 你: %d %q", cols, line.String())
	}
	if line.backspace() != 0 {
		t.Fatal("empty backspace")
	}
}
