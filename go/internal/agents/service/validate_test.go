package service

import (
	"encoding/json"
	"os"
	"testing"

	"cakerdesk/internal/platform/apperr"
)

func TestT_agent_version_rules(t *testing.T) {
	root, err := os.ReadFile("../../../../schemas/agent_version.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(root) != string(schemaDocument) {
		t.Fatal("schemas/agent_version.schema.json 与嵌入副本不一致")
	}
	okSkills := func(string) (SkillHit, error) { return SkillHit{}, nil }
	okSecrets := func(string) (bool, error) { return false, nil }
	if err := Validate(must(validConfig()), []string{"gpt-4.5"}, okSkills, okSecrets); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		edit   func(map[string]any)
		code   string
		skill  SkillLookup
		secret SecretLookup
	}{
		{name: "V1-suffix", code: "agent_version.model_alias_forbidden", edit: func(m map[string]any) { m["model"].(map[string]any)["model_id"] = "gpt-4.5-latest" }},
		{name: "V1-alias", code: "agent_version.model_alias_forbidden", edit: func(m map[string]any) { m["model"].(map[string]any)["model_id"] = "gpt-4.5" }},
		{name: "V2-unknown", code: "agent_version.unknown_tool", edit: func(m map[string]any) { m["tools"] = []any{map[string]any{"name": "rm", "approval": "never"}} }},
		{name: "V2-duplicate", code: "agent_version.unknown_tool", edit: func(m map[string]any) {
			m["tools"] = []any{map[string]any{"name": "bash", "approval": "never"}, map[string]any{"name": "bash", "approval": "always"}}
		}},
		{name: "V3", code: "agent_version.skill_not_published", edit: func(m map[string]any) {
			m["skills"] = []any{map[string]any{"name": "csv-cleaning", "hash": "sha256:missing", "entry_allowed": true}}
		}},
		{name: "V4", code: "agent_version.skill_tool_mismatch", skill: func(string) (SkillHit, error) {
			return SkillHit{FrontName: "csv-cleaning", Allowed: []string{"bash"}, Declared: true, Found: true}, nil
		}, edit: func(m map[string]any) {
			m["skills"] = []any{map[string]any{"name": "csv-cleaning", "hash": "sha256:abc", "entry_allowed": true}}
			m["tools"] = []any{map[string]any{"name": "read_file", "approval": "never"}}
		}},
		{name: "V5", code: "agent_version.image_not_pinned", edit: func(m map[string]any) { m["sandbox"].(map[string]any)["image"] = "cakerdesk/sandbox:py312" }},
		{name: "V6", code: "agent_version.secret_missing", edit: func(m map[string]any) { m["secrets"] = []any{"MISSING_TOKEN"} }},
		{name: "V7", code: "agent_version.invalid_schema", edit: func(m map[string]any) {
			m["io"].(map[string]any)["input_schema"] = map[string]any{"type": "not-a-type"}
		}},
		{name: "V8", code: "agent_version.out_of_range", edit: func(m map[string]any) { m["context"].(map[string]any)["compaction_trigger_ratio"] = 0.1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.edit(cfg)
			skills := tc.skill
			if skills == nil {
				skills = okSkills
			}
			secrets := tc.secret
			if secrets == nil {
				secrets = okSecrets
			}
			err := Validate(must(cfg), []string{"gpt-4.5"}, skills, secrets)
			ae, ok := apperr.As(err)
			if !ok || ae.Code != tc.code {
				t.Fatalf("got %v want %s", err, tc.code)
			}
			if ae.Status != 422 {
				t.Fatalf("status %d", ae.Status)
			}
		})
	}
}

func validConfig() map[string]any {
	return map[string]any{
		"identity": map[string]any{"display_name": "数据整理助手", "system_prompt": "你是助手", "language": "zh-CN"},
		"model":    map[string]any{"provider": "openai_compatible", "base_url_ref": "llm.default", "model_id": "gpt-4.5-2025-04-14", "temperature": 0.2, "max_output_tokens": 4096, "context_window": 128000},
		"tools":    []any{map[string]any{"name": "read_file", "approval": "never"}},
		"skills":   []any{},
		"memory": map[string]any{
			"session": map[string]any{"enabled": true, "auto_extract": true, "max_entries": 300, "digest_tokens": 500},
			"agent":   map[string]any{"enabled": true, "inject_top_k": 6, "inject_tokens": 1000, "consolidate_after_entries": 20},
		},
		"context": map[string]any{"compaction_trigger_ratio": 0.7, "keep_recent_messages": 12, "tool_output_offload_chars": 8000},
		"runtime": map[string]any{"max_duration_seconds": 3600, "max_model_calls": 200, "max_tool_calls": 500, "max_total_tokens": 2000000, "recursion_limit": 1000, "loop_warn_repeats": 3, "loop_fail_repeats": 5, "max_attempts": 3, "goal": map[string]any{"enabled": true, "max_continuations": 8, "no_progress_repeats": 2}},
		"sandbox": map[string]any{"image": "cakerdesk/sandbox@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "cpus": 1.0, "memory_mb": 1024, "pids_limit": 256, "exec_timeout_seconds": 120, "max_exec_timeout_seconds": 600},
		"io":      map[string]any{"input_schema": map[string]any{"type": "object"}, "output_schema": map[string]any{"type": "object"}},
		"secrets": []any{},
	}
}

func must(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
