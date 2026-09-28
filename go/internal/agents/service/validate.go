package service

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"cakerdesk/internal/platform/apperr"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed agent_version.schema.json
var schemaDocument []byte

var (
	schemaOnce  sync.Once
	agentSchema *jsonschema.Schema
	schemaLoad  error
)

var (
	aliasPattern = regexp.MustCompile(`(latest|preview)$`)
	imagePattern = regexp.MustCompile(`^.+@sha256:[0-9a-fA-F]{64}$`)
	secretRef    = regexp.MustCompile(`\{\{secret\.([A-Z][A-Z0-9_]{0,63})\}\}`)
)

var registry = map[string]struct{}{
	"load_skill": {}, "list_files": {}, "read_file": {}, "write_file": {}, "edit_file": {},
	"bash": {}, "run_python": {}, "deliver_artifact": {}, "submit_result": {}, "http_request": {},
	"ask_user": {}, "spawn_subagents": {}, "memory_search": {}, "memory_read": {},
	"memory_write": {}, "memory_update": {}, "memory_forget": {},
}

var baseline = map[string]struct{}{
	"load_skill": {}, "submit_result": {}, "list_files": {}, "read_file": {}, "ask_user": {},
	"memory_search": {}, "memory_read": {}, "memory_write": {}, "memory_update": {}, "memory_forget": {},
}

type SkillHit struct {
	FrontName string
	Allowed   []string
	Declared  bool
	Found     bool
}

type SkillLookup func(hash string) (SkillHit, error)
type SecretLookup func(name string) (bool, error)

func loadSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		var doc any
		if err := json.Unmarshal(schemaDocument, &doc); err != nil {
			schemaLoad = err
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		if err := c.AddResource("agent_version.schema.json", doc); err != nil {
			schemaLoad = err
			return
		}
		agentSchema, schemaLoad = c.Compile("agent_version.schema.json")
	})
	return agentSchema, schemaLoad
}

func Validate(raw []byte, aliases []string, skills SkillLookup, secrets SecretLookup) error {
	sch, err := loadSchema()
	if err != nil {
		return err
	}
	var inst any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&inst); err != nil {
		return apperr.JSON()
	}
	if err := sch.Validate(inst); err != nil {
		return apperr.Wrap(400, "request.validation_failed", "AgentVersion 配置不符合 schema", schemaDetails(err))
	}
	var cfg versionConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return apperr.JSON()
	}
	if err := ruleModel(cfg, aliases); err != nil {
		return err
	}
	if err := ruleTools(cfg); err != nil {
		return err
	}
	if err := ruleSkills(cfg, skills); err != nil {
		return err
	}
	if err := ruleImage(cfg); err != nil {
		return err
	}
	if err := ruleSecrets(cfg, secrets); err != nil {
		return err
	}
	if err := ruleSchemas(cfg); err != nil {
		return err
	}
	return ruleRanges(cfg)
}

func ruleModel(cfg versionConfig, aliases []string) error {
	id := cfg.Model.ModelID
	if aliasPattern.MatchString(id) {
		return versionErr("agent_version.model_alias_forbidden", "model.model_id 不能以 latest 或 preview 结尾")
	}
	for _, alias := range aliases {
		if id == alias {
			return versionErr("agent_version.model_alias_forbidden", "model.model_id 不能使用家族别名 "+alias)
		}
	}
	return nil
}

func ruleTools(cfg versionConfig) error {
	seen := map[string]struct{}{}
	for _, tool := range cfg.Tools {
		if _, ok := registry[tool.Name]; !ok {
			return versionErr("agent_version.unknown_tool", "未知工具 "+tool.Name)
		}
		if _, ok := seen[tool.Name]; ok {
			return versionErr("agent_version.unknown_tool", "工具重复 "+tool.Name)
		}
		seen[tool.Name] = struct{}{}
	}
	return nil
}

func ruleSkills(cfg versionConfig, skills SkillLookup) error {
	allowed := map[string]struct{}{}
	for name := range baseline {
		allowed[name] = struct{}{}
	}
	for _, tool := range cfg.Tools {
		allowed[tool.Name] = struct{}{}
	}
	for _, sk := range cfg.Skills {
		hit, err := skills(sk.Hash)
		if err != nil {
			return err
		}
		if !hit.Found || hit.FrontName != sk.Name {
			return versionErr("agent_version.skill_not_published", "Skill 未发布或名称与 front matter 不一致: "+sk.Name)
		}
		if !hit.Declared {
			continue
		}
		for _, name := range hit.Allowed {
			if _, ok := allowed[name]; !ok {
				return versionErr("agent_version.skill_tool_mismatch", fmt.Sprintf("Skill %s 的工具 %s 不在 Agent 工具与基础工具的并集中", sk.Name, name))
			}
		}
	}
	return nil
}

func ruleImage(cfg versionConfig) error {
	if !imagePattern.MatchString(cfg.Sandbox.Image) {
		return versionErr("agent_version.image_not_pinned", "sandbox.image 必须是 name@sha256:<64 hex> 形式的 digest")
	}
	return nil
}

func ruleSecrets(cfg versionConfig, secrets SecretLookup) error {
	declared := map[string]struct{}{}
	for _, name := range cfg.Secrets {
		ok, err := secrets(name)
		if err != nil {
			return err
		}
		if !ok {
			return versionErr("agent_version.secret_missing", "Secret 不存在: "+name)
		}
		declared[name] = struct{}{}
	}
	for _, tool := range cfg.Tools {
		if tool.Options == nil {
			continue
		}
		blob, err := json.Marshal(tool.Options)
		if err != nil {
			return err
		}
		for _, match := range secretRef.FindAllStringSubmatch(string(blob), -1) {
			name := match[1]
			if _, ok := declared[name]; !ok {
				return versionErr("agent_version.secret_missing", "模板引用的 Secret 未声明: "+name)
			}
		}
	}
	return nil
}

func ruleSchemas(cfg versionConfig) error {
	if err := compileSchema("input_schema", cfg.IO.Input); err != nil {
		return versionErr("agent_version.invalid_schema", err.Error())
	}
	if err := compileSchema("output_schema", cfg.IO.Output); err != nil {
		return versionErr("agent_version.invalid_schema", err.Error())
	}
	return nil
}

func compileSchema(name string, raw json.RawMessage) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s 不是 JSON 对象", name)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	url := name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if _, err := c.Compile(url); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func ruleRanges(cfg versionConfig) error {
	ratio := cfg.Context.CompactionTriggerRatio
	if ratio < 0.5 || ratio > 0.9 {
		return versionErr("agent_version.out_of_range", "compaction_trigger_ratio 必须在 [0.5, 0.9]")
	}
	keep := cfg.Context.KeepRecentMessages
	if keep < 4 || keep > 50 {
		return versionErr("agent_version.out_of_range", "keep_recent_messages 必须在 [4, 50]")
	}
	cont := cfg.Runtime.Goal.MaxContinuations
	if cont < 0 || cont > 8 {
		return versionErr("agent_version.out_of_range", "max_continuations 必须在 [0, 8]")
	}
	attempts := cfg.Runtime.MaxAttempts
	if attempts < 1 || attempts > 5 {
		return versionErr("agent_version.out_of_range", "max_attempts 必须在 [1, 5]")
	}
	if cfg.Sandbox.ExecTimeout > cfg.Sandbox.MaxExecTimeout || cfg.Sandbox.MaxExecTimeout > 600 {
		return versionErr("agent_version.out_of_range", "exec_timeout_seconds 必须小于等于 max_exec_timeout_seconds，且不超过 600")
	}
	return nil
}

func versionErr(code, message string) *apperr.Error {
	return apperr.New(422, code, message)
}

func schemaDetails(err error) []apperr.Detail {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []apperr.Detail{{Path: "/", Message: err.Error()}}
	}
	var out []apperr.Detail
	var walk func(*jsonschema.ValidationError)
	walk = func(v *jsonschema.ValidationError) {
		if len(v.Causes) == 0 {
			out = append(out, apperr.Detail{Path: "/" + strings.Join(v.InstanceLocation, "/"), Message: v.Error()})
			return
		}
		for _, c := range v.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(out) == 0 {
		out = append(out, apperr.Detail{Path: "/", Message: err.Error()})
	}
	return out
}

type versionConfig struct {
	Model struct {
		ModelID string `json:"model_id"`
	} `json:"model"`
	Tools []struct {
		Name    string         `json:"name"`
		Options map[string]any `json:"options"`
	} `json:"tools"`
	Skills []struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	} `json:"skills"`
	Context struct {
		CompactionTriggerRatio float64 `json:"compaction_trigger_ratio"`
		KeepRecentMessages     int     `json:"keep_recent_messages"`
	} `json:"context"`
	Runtime struct {
		MaxAttempts int `json:"max_attempts"`
		Goal        struct {
			MaxContinuations int `json:"max_continuations"`
		} `json:"goal"`
	} `json:"runtime"`
	Sandbox struct {
		Image          string `json:"image"`
		ExecTimeout    int    `json:"exec_timeout_seconds"`
		MaxExecTimeout int    `json:"max_exec_timeout_seconds"`
	} `json:"sandbox"`
	IO struct {
		Input  json.RawMessage `json:"input_schema"`
		Output json.RawMessage `json:"output_schema"`
	} `json:"io"`
	Secrets []string `json:"secrets"`
}
