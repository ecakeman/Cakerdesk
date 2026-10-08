package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Options struct {
	BaseURL string
	Out     io.Writer
	In      io.Reader
}

func Execute(args []string, opt Options) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: cakerdesk project|thread|run|artifact|memory ...")
	}
	switch args[0] {
	case "project":
		return project(args[1:], opt)
	case "thread":
		return thread(args[1:], opt)
	case "run":
		return run(args[1:], opt)
	case "artifact":
		return artifact(args[1:], opt)
	case "memory":
		return memory(args[1:], opt)
	default:
		return fmt.Errorf("未知命令 %s", args[0])
	}
}

func project(args []string, opt Options) error {
	if len(args) == 0 {
		return fmt.Errorf("project create|list")
	}
	switch args[0] {
	case "list":
		return get(opt, "/api/projects")
	case "create":
		name := flagValue(args[1:], "--name")
		if name == "" {
			return fmt.Errorf("project create --name")
		}
		return post(opt, "/api/projects", map[string]string{"name": name})
	default:
		return fmt.Errorf("project create|list")
	}
}

func thread(args []string, opt Options) error {
	if len(args) == 0 {
		return fmt.Errorf("thread create|list")
	}
	switch args[0] {
	case "list":
		projectID := flagValue(args[1:], "--project")
		if projectID == "" {
			return fmt.Errorf("thread list --project")
		}
		return get(opt, "/api/projects/"+projectID+"/threads")
	case "create":
		projectID := flagValue(args[1:], "--project")
		title := flagValue(args[1:], "--title")
		if projectID == "" || title == "" {
			return fmt.Errorf("thread create --project --title")
		}
		return post(opt, "/api/projects/"+projectID+"/threads", map[string]string{"title": title})
	default:
		return fmt.Errorf("thread create|list")
	}
}

func run(args []string, opt Options) error {
	if len(args) == 0 {
		return fmt.Errorf("run start|show|watch|resume|cancel")
	}
	switch args[0] {
	case "start":
		threadID := flagValue(args[1:], "--thread")
		projectID := flagValue(args[1:], "--project")
		goal := flagValue(args[1:], "--goal")
		if threadID == "" || projectID == "" || goal == "" {
			return fmt.Errorf("run start --thread --project --goal")
		}
		return post(opt, "/api/threads/"+threadID+"/runs?project_id="+projectID, map[string]string{"goal": goal})
	case "show":
		return showRun(opt, need(args[1:], "--run"))
	case "resume":
		return post(opt, "/api/runs/"+need(args[1:], "--run")+"/resume", map[string]string{})
	case "cancel":
		return post(opt, "/api/runs/"+need(args[1:], "--run")+"/cancel", map[string]string{})
	case "watch":
		return watch(opt, need(args[1:], "--run"))
	default:
		return fmt.Errorf("run start|show|watch|resume|cancel")
	}
}

func artifact(args []string, opt Options) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("artifact list --run")
	}
	return get(opt, "/api/runs/"+need(args[1:], "--run")+"/artifacts")
}

func memory(args []string, opt Options) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("memory list --project")
	}
	return get(opt, "/api/projects/"+need(args[1:], "--project")+"/memory")
}

func watch(opt Options, runID string) error {
	_, err := followRun(context.Background(), opt, runID)
	return err
}

func followRun(ctx context.Context, opt Options, runID string) (string, error) {
	if runID == "" {
		return "", fmt.Errorf("run watch --run")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(opt.BaseURL, "/")+"/api/runs/"+runID+"/events?after_seq=0", nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		return "", fmt.Errorf("watch %d %s", response.StatusCode, raw)
	}
	scanner := bufio.NewScanner(response.Body)
	var steps []stepView
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return "", err
		}
		text, next := projectLine(steps, event.Type, event.Payload)
		steps = next
		fmt.Fprintln(opt.Out, text)
		if isTerminal(event.Type) {
			return strings.TrimPrefix(event.Type, "run."), nil
		}
	}
	return "", scanner.Err()
}

func isTerminal(eventType string) bool {
	switch eventType {
	case "run.completed", "run.failed", "run.cancelled":
		return true
	default:
		return false
	}
}

func showRun(opt Options, runID string) error {
	if runID == "" {
		return fmt.Errorf("run show --run")
	}
	response, err := http.Get(strings.TrimRight(opt.BaseURL, "/") + "/api/runs/" + runID)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	fmt.Fprintln(opt.Out, formatShow(raw))
	if response.StatusCode >= 300 {
		return fmt.Errorf("http %d", response.StatusCode)
	}
	return nil
}

func get(opt Options, path string) error {
	response, err := http.Get(strings.TrimRight(opt.BaseURL, "/") + path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return writeResponse(opt, response)
}

func post(opt Options, path string, body any) error {
	raw, _ := json.Marshal(body)
	response, err := http.Post(strings.TrimRight(opt.BaseURL, "/")+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return writeResponse(opt, response)
}

func writeResponse(opt Options, response *http.Response) error {
	raw, _ := io.ReadAll(response.Body)
	fmt.Fprintln(opt.Out, strings.TrimSpace(string(raw)))
	if response.StatusCode >= 300 {
		return fmt.Errorf("http %d", response.StatusCode)
	}
	return nil
}

func flagValue(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func need(args []string, name string) string {
	return flagValue(args, name)
}
