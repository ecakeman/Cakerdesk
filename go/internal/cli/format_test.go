package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWatchProjectsHumanLines(t *testing.T) {
	events := []string{
		`{"type":"plan.updated","payload":{"plan":{"steps":[{"id":"s1","title":"阅读 notes.txt","status":"completed"},{"id":"s4","title":"撰写 report.md","status":"completed"}]}}}`,
		`{"type":"verification.completed","payload":{"passed":false,"findings":[{"criterion":"结论","status":"failed","evidence":"正文没有结论"}]}}`,
		`{"type":"replan.started","payload":{"reason":"verification_failed"}}`,
		`{"type":"plan.updated","payload":{"plan":{"steps":[{"id":"s1","title":"阅读 notes.txt","status":"completed"},{"id":"s4","title":"补写结论","status":"pending"},{"id":"s5","title":"再次提交","status":"pending"}]}}}`,
		`{"type":"memory.written","payload":{"kind":"lesson","summary":"遗漏了结论"}}`,
		`{"type":"run.completed","payload":{"summary":"任务完成","artifacts":["artifacts/report.md"]}}`,
	}
	var steps []stepView
	var lines []string
	for _, raw := range events {
		var event struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		line, next := projectLine(steps, event.Type, event.Payload)
		steps = next
		lines = append(lines, line)
		if strings.Contains(line, "{") {
			t.Fatalf("raw json in %s", line)
		}
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"verification.completed 未通过 结论", "replan.started verification_failed", "s1 阅读 notes.txt completed 保留", "s4 补写结论 pending 重新打开", "s5 再次提交 pending 新增", "memory.written lesson 遗漏了结论", "run.completed 任务完成 artifacts/report.md"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
}
