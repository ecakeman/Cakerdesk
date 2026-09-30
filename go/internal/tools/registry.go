package tools

var registry = map[string]struct{}{
	"submit_result": {},
}

func Known(name string) bool {
	_, ok := registry[name]
	return ok
}
