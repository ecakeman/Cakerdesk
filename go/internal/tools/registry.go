package tools

import "encoding/json"

type Spec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Idempotent  bool
}

var registry = map[string]Spec{
	"submit_result": {
		Name:        "submit_result",
		Description: "提交这次 Run 的最终结果。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {"type": "string", "minLength": 1, "maxLength": 4000},
    "data": {"type": "object"}
  },
  "required": ["summary"],
  "additionalProperties": false
}`),
		Idempotent: true,
	},
}

// 配置里的工具名必须在这里。现在只有 submit_result，写上 bash 会在发布时 400，而不是执行时才发现。
func Known(name string) bool {
	_, ok := registry[name]
	return ok
}

func Get(name string) (Spec, bool) {
	spec, ok := registry[name]
	return spec, ok
}
