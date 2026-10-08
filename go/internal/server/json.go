package server

import "cakerdesk/internal/db"

func runJSON(run db.GetRunRow) map[string]string {
	return map[string]string{
		"id":                    run.ID,
		"project_id":            run.ProjectID,
		"thread_id":             run.ThreadID,
		"goal":                  run.Goal,
		"status":                run.Status,
		"plan_snapshot":         run.PlanSnapshot,
		"verification_snapshot": run.VerificationSnapshot,
		"artifact_paths":        run.ArtifactPaths,
	}
}

func projectsJSON(items []db.ListProjectsRow) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]string{"id": item.ID, "name": item.Name})
	}
	return out
}

func threadsJSON(items []db.ListThreadsRow) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]string{"id": item.ID, "title": item.Title})
	}
	return out
}

func messagesJSON(items []db.ListMessagesRow) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]string{"role": item.Role, "content": item.Content})
	}
	return out
}
