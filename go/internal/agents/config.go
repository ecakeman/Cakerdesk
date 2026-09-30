package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/ecakeman/cakerdesk/internal/tools"
)

type Limits struct {
	MaxLLMCalls    int `json:"max_llm_calls"`
	MaxToolCalls   int `json:"max_tool_calls"`
	MaxTotalTokens int `json:"max_total_tokens"`
	MaxDurationS   int `json:"max_duration_s"`
	MaxAttempts    int `json:"max_attempts"`
}

type Context struct {
	CompactThresholdTokens int `json:"compact_threshold_tokens"`
	KeepLastMessages       int `json:"keep_last_messages"`
}

type Config struct {
	Model        string   `json:"model"`
	SystemPrompt string   `json:"system_prompt"`
	Tools        []string `json:"tools"`
	Skills       []string `json:"skills"`
	Limits       Limits   `json:"limits"`
	Context      Context  `json:"context"`
}

type rawConfig struct {
	Model        *string          `json:"model"`
	SystemPrompt *string          `json:"system_prompt"`
	Tools        []string         `json:"tools"`
	Skills       []string         `json:"skills"`
	Limits       *json.RawMessage `json:"limits"`
	Context      *json.RawMessage `json:"context"`
}

// ParseAndNormalize 校验 AgentConfig、补缺省、按结构体字段顺序序列化后再算 sha256。
func ParseAndNormalize(raw json.RawMessage) (Config, string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in rawConfig
	if err := dec.Decode(&in); err != nil {
		path := unknownFieldPath(err)
		if path != "" {
			return Config{}, "", apperr.New(http.StatusBadRequest, "invalid_config", "未知字段 "+path)
		}
		return Config{}, "", apperr.New(http.StatusBadRequest, "invalid_config", "config")
	}
	cfg := Config{
		Limits: Limits{
			MaxLLMCalls:    40,
			MaxToolCalls:   80,
			MaxTotalTokens: 200000,
			MaxDurationS:   1800,
			MaxAttempts:    3,
		},
		Context: Context{
			CompactThresholdTokens: 12000,
			KeepLastMessages:       8,
		},
		Skills: []string{},
	}
	if in.Model == nil || *in.Model == "" {
		return Config{}, "", invalid("model")
	}
	cfg.Model = *in.Model
	if in.SystemPrompt == nil {
		return Config{}, "", invalid("system_prompt")
	}
	cfg.SystemPrompt = *in.SystemPrompt
	if n := len(cfg.SystemPrompt); n < 1 || n > 20000 {
		return Config{}, "", invalid("system_prompt")
	}
	if in.Tools == nil {
		return Config{}, "", invalid("tools")
	}
	cfg.Tools = in.Tools
	if in.Skills != nil {
		cfg.Skills = in.Skills
	}
	if in.Limits != nil {
		lim, err := decodeLimits(*in.Limits)
		if err != nil {
			return Config{}, "", err
		}
		cfg.Limits = lim
	}
	if in.Context != nil {
		ctx, err := decodeContext(*in.Context)
		if err != nil {
			return Config{}, "", err
		}
		cfg.Context = ctx
	}
	if err := validate(cfg); err != nil {
		return Config{}, "", err
	}
	norm, err := json.Marshal(cfg)
	if err != nil {
		return Config{}, "", err
	}
	sum := sha256.Sum256(norm)
	return cfg, hex.EncodeToString(sum[:]), nil
}

func decodeLimits(raw json.RawMessage) (Limits, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var lim Limits
	if err := dec.Decode(&lim); err != nil {
		path := unknownFieldPath(err)
		if path != "" {
			return Limits{}, apperr.New(http.StatusBadRequest, "invalid_config", "未知字段 limits."+path)
		}
		return Limits{}, invalid("limits")
	}
	return lim, nil
}

func decodeContext(raw json.RawMessage) (Context, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var ctx Context
	if err := dec.Decode(&ctx); err != nil {
		path := unknownFieldPath(err)
		if path != "" {
			return Context{}, apperr.New(http.StatusBadRequest, "invalid_config", "未知字段 context."+path)
		}
		return Context{}, invalid("context")
	}
	return ctx, nil
}

func validate(cfg Config) error {
	if len(cfg.Tools) == 0 {
		return invalid("tools")
	}
	seen := map[string]struct{}{}
	hasSubmit := false
	for _, name := range cfg.Tools {
		if _, dup := seen[name]; dup {
			return invalid("tools")
		}
		seen[name] = struct{}{}
		if !tools.Known(name) {
			return invalid("tools")
		}
		if name == "submit_result" {
			hasSubmit = true
		}
	}
	if !hasSubmit {
		return invalid("tools")
	}
	if cfg.Limits.MaxLLMCalls < 1 || cfg.Limits.MaxLLMCalls > 500 {
		return invalid("limits.max_llm_calls")
	}
	if cfg.Limits.MaxToolCalls < 1 || cfg.Limits.MaxToolCalls > 1000 {
		return invalid("limits.max_tool_calls")
	}
	if cfg.Limits.MaxTotalTokens < 1000 || cfg.Limits.MaxTotalTokens > 5000000 {
		return invalid("limits.max_total_tokens")
	}
	if cfg.Limits.MaxDurationS < 10 || cfg.Limits.MaxDurationS > 86400 {
		return invalid("limits.max_duration_s")
	}
	if cfg.Limits.MaxAttempts < 1 || cfg.Limits.MaxAttempts > 10 {
		return invalid("limits.max_attempts")
	}
	if cfg.Context.CompactThresholdTokens < 2000 || cfg.Context.CompactThresholdTokens > 200000 {
		return invalid("context.compact_threshold_tokens")
	}
	if cfg.Context.KeepLastMessages < 2 || cfg.Context.KeepLastMessages > 50 {
		return invalid("context.keep_last_messages")
	}
	return nil
}

func invalid(path string) error {
	return apperr.New(http.StatusBadRequest, "invalid_config", path)
}

func unknownFieldPath(err error) string {
	const prefix = "json: unknown field "
	s := err.Error()
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	return strings.Trim(strings.TrimPrefix(s, prefix), `"`)
}

func MustJSON(cfg Config) json.RawMessage {
	b, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return b
}

func ParseStored(raw json.RawMessage) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("stored config: %w", err)
	}
	return cfg, nil
}
