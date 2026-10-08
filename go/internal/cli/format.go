package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

type stepView struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

func projectLine(previous []stepView, eventType string, payload json.RawMessage) (string, []stepView) {
	switch eventType {
	case "tool.completed":
		var body struct {
			Tool    string `json:"tool"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(payload, &body)
		return strings.TrimSpace("tool.completed " + body.Tool + " " + body.Summary), previous
	case "tool.started":
		var body struct {
			Tool string `json:"tool"`
		}
		_ = json.Unmarshal(payload, &body)
		return "tool.started " + body.Tool, previous
	case "verification.completed":
		var body struct {
			Passed   bool `json:"passed"`
			Findings []struct {
				Criterion string `json:"criterion"`
				Status    string `json:"status"`
				Evidence  string `json:"evidence"`
			} `json:"findings"`
		}
		_ = json.Unmarshal(payload, &body)
		if body.Passed {
			return "verification.completed 通过", previous
		}
		var failed []string
		for _, item := range body.Findings {
			if item.Status == "failed" {
				failed = append(failed, item.Criterion+" "+item.Evidence)
			}
		}
		return "verification.completed 未通过 " + strings.Join(failed, "；"), previous
	case "plan.updated":
		steps := readSteps(payload)
		return "plan.updated " + formatPlan(previous, steps), steps
	case "replan.started":
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(payload, &body)
		return "replan.started " + body.Reason, previous
	case "subagent.started":
		var body struct {
			Task string `json:"task"`
		}
		_ = json.Unmarshal(payload, &body)
		return "subagent.started " + body.Task, previous
	case "subagent.completed":
		var body struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(payload, &body)
		return "subagent.completed " + body.Summary, previous
	case "memory.written":
		var body struct {
			Kind    string `json:"kind"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(payload, &body)
		return "memory.written " + body.Kind + " " + body.Summary, previous
	case "model.message":
		var body struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(payload, &body)
		return "model.message " + body.Content, previous
	case "run.started":
		var body struct {
			Goal string `json:"goal"`
		}
		_ = json.Unmarshal(payload, &body)
		return "run.started " + body.Goal, previous
	case "run.completed":
		var body struct {
			Summary   string   `json:"summary"`
			Artifacts []string `json:"artifacts"`
		}
		_ = json.Unmarshal(payload, &body)
		return strings.TrimSpace("run.completed " + body.Summary + " " + strings.Join(body.Artifacts, " ")), previous
	case "run.failed":
		var body struct {
			Reason     string `json:"reason"`
			Message    string `json:"message"`
			LastOutput string `json:"last_output"`
		}
		_ = json.Unmarshal(payload, &body)
		return strings.TrimSpace("run.failed " + body.Reason + " " + body.Message + " " + body.LastOutput), previous
	case "run.cancelled":
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(payload, &body)
		return "run.cancelled " + body.Reason, previous
	default:
		return eventType, previous
	}
}

func formatShow(raw []byte) string {
	var run struct {
		Status               string `json:"status"`
		Goal                 string `json:"goal"`
		PlanSnapshot         string `json:"plan_snapshot"`
		VerificationSnapshot string `json:"verification_snapshot"`
		ArtifactPaths        string `json:"artifact_paths"`
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		return string(raw)
	}
	var lines []string
	lines = append(lines, "status: "+run.Status)
	if run.Goal != "" {
		lines = append(lines, "goal: "+run.Goal)
	}
	if strings.TrimSpace(run.PlanSnapshot) != "" {
		lines = append(lines, "plan: "+formatPlan(nil, readSteps(json.RawMessage(run.PlanSnapshot))))
	}
	if strings.TrimSpace(run.VerificationSnapshot) != "" {
		line, _ := projectLine(nil, "verification.completed", json.RawMessage(run.VerificationSnapshot))
		lines = append(lines, "verification: "+strings.TrimPrefix(line, "verification.completed "))
	}
	var paths []string
	_ = json.Unmarshal([]byte(run.ArtifactPaths), &paths)
	if len(paths) > 0 {
		lines = append(lines, "artifacts: "+strings.Join(paths, " "))
	}
	return strings.Join(lines, "\n")
}

func readSteps(payload json.RawMessage) []stepView {
	var wrapped struct {
		Plan struct {
			Steps []stepView `json:"steps"`
		} `json:"plan"`
		Steps []stepView `json:"steps"`
	}
	_ = json.Unmarshal(payload, &wrapped)
	steps := wrapped.Plan.Steps
	if len(steps) == 0 {
		steps = wrapped.Steps
	}
	return steps
}

func formatPlan(previous, steps []stepView) string {
	old := map[string]stepView{}
	for _, step := range previous {
		old[step.ID] = step
	}
	parts := make([]string, 0, len(steps))
	for _, step := range steps {
		note := ""
		if previous != nil {
			before, seen := old[step.ID]
			switch {
			case !seen:
				note = " 新增"
			case before.Status == "completed" && step.Status == "completed" && before.Title == step.Title:
				note = " 保留"
			case before.Status != step.Status && step.Status == "pending":
				note = " 重新打开"
			}
		}
		parts = append(parts, fmt.Sprintf("%s %s %s%s", step.ID, step.Title, step.Status, note))
	}
	return strings.Join(parts, "; ")
}
