package tools

var registry = map[string]struct{}{
	"submit_result": {},
}

func Known(name string) bool { // A2 注册表只有 submit_result。
	_, ok := registry[name]
	return ok
}
