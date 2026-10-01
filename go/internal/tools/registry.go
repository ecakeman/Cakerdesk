package tools

var registry = map[string]struct{}{
	"submit_result": {},
}

// 配置里的工具名必须在这里。现在只有 submit_result，写上 bash 会在发布时 400，而不是执行时才发现。
func Known(name string) bool {
	_, ok := registry[name]
	return ok
}
