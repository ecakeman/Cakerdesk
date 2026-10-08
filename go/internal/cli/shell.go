package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

type shellState struct {
	projectID   string
	projectName string
	threadID    string
	threadTitle string
	runID       string
}

func Shell(opt Options) error {
	in := opt.In
	if in == nil {
		in = strings.NewReader("")
	}
	session := openTerminal(in)
	defer session.Close()
	var gate sync.Mutex
	var activeRun string
	var cancelWatch context.CancelFunc
	var cancelSent bool
	if session != nil {
		session.setHandler(func() bool {
			gate.Lock()
			defer gate.Unlock()
			if activeRun == "" {
				return false
			}
			if cancelSent {
				if cancelWatch != nil {
					cancelWatch()
				}
				return false
			}
			cancelSent = true
			id := activeRun
			go func() {
				_, _ = postBody(opt, "/api/runs/"+id+"/cancel", map[string]string{})
			}()
			return true
		})
	}
	keys := input{session: session, reader: bufio.NewReader(in), out: opt.Out}
	state, err := bindContext(opt, keys)
	if err != nil {
		return err
	}
	fmt.Fprintf(opt.Out, "Cakerdesk\nProject: %s\nThread: %s\n\n", state.projectName, state.threadID)
	for {
		writePrompt(opt.Out)
		line, err := keys.line()
		if err == io.EOF {
			if session == nil {
				fmt.Fprintln(opt.Out)
			}
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			stop, err := slash(opt, keys, &state, line)
			if err != nil {
				fmt.Fprintln(opt.Out, err.Error())
			}
			if stop {
				return nil
			}
			continue
		}
		if err := state.runGoal(opt, line, &gate, &activeRun, &cancelWatch, &cancelSent); err != nil {
			fmt.Fprintln(opt.Out, err.Error())
		}
		fmt.Fprintln(opt.Out)
	}
}

type input struct {
	session *terminalSession
	reader  *bufio.Reader
	out     io.Writer
}

func (keys input) line() (string, error) {
	if keys.session != nil {
		return keys.session.ReadLine(keys.out)
	}
	return keys.reader.ReadString('\n')
}

func slash(opt Options, keys input, state *shellState, line string) (bool, error) {
	fields := strings.Fields(line)
	switch fields[0] {
	case "/help":
		fmt.Fprintln(opt.Out, "直接输入任务。/new 新线程  /switch 切换  /memory 记忆  /artifact 产物  /cancel 取消  /resume 恢复  /exit 退出")
	case "/exit":
		return true, nil
	case "/new":
		title, err := ask(opt, keys, "Thread:")
		if err != nil {
			return false, err
		}
		item, err := createThread(opt, state.projectID, title)
		if err != nil {
			return false, err
		}
		state.threadID = item.ID
		state.threadTitle = item.Name
		state.runID = ""
		fmt.Fprintf(opt.Out, "Thread: %s\n", state.threadID)
	case "/switch":
		next, err := bindContext(opt, keys)
		if err != nil {
			return false, err
		}
		*state = next
		fmt.Fprintf(opt.Out, "Project: %s\nThread: %s\n", state.projectName, state.threadID)
	case "/memory":
		raw, err := getBody(opt, "/api/projects/"+state.projectID+"/memory")
		if err != nil {
			return false, err
		}
		fmt.Fprintln(opt.Out, formatMemory(raw))
	case "/artifact":
		if state.runID == "" {
			return false, fmt.Errorf("还没有 Run")
		}
		raw, err := getBody(opt, "/api/runs/"+state.runID+"/artifacts")
		if err != nil {
			return false, err
		}
		fmt.Fprintln(opt.Out, strings.TrimSpace(string(raw)))
	case "/cancel":
		if state.runID == "" {
			return false, fmt.Errorf("还没有 Run")
		}
		if _, err := postBody(opt, "/api/runs/"+state.runID+"/cancel", map[string]string{}); err != nil {
			return false, err
		}
		fmt.Fprintln(opt.Out, "cancel_requested")
	case "/resume":
		if state.runID == "" {
			return false, fmt.Errorf("No resumable run.")
		}
		if _, err := postBody(opt, "/api/runs/"+state.runID+"/resume", map[string]string{}); err != nil {
			if strings.Contains(err.Error(), "No resumable checkpoint") {
				return false, fmt.Errorf("No resumable checkpoint.")
			}
			return false, err
		}
		if _, err := followRun(context.Background(), opt, state.runID); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("未知命令 %s", fields[0])
	}
	return false, nil
}

func (state *shellState) runGoal(opt Options, goal string, gate *sync.Mutex, activeRun *string, cancelWatch *context.CancelFunc, cancelSent *bool) error {
	raw, err := postBody(opt, "/api/threads/"+state.threadID+"/runs?project_id="+state.projectID, map[string]string{"goal": goal})
	if err != nil {
		return err
	}
	var run struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" {
		return fmt.Errorf("没有得到 run id")
	}
	state.runID = run.ID
	ctx, cancel := context.WithCancel(context.Background())
	gate.Lock()
	*activeRun = run.ID
	*cancelSent = false
	*cancelWatch = cancel
	gate.Unlock()
	defer func() {
		gate.Lock()
		*activeRun = ""
		*cancelWatch = nil
		gate.Unlock()
		cancel()
	}()
	_, err = followRun(ctx, opt, run.ID)
	return err
}

func writePrompt(out io.Writer) {
	fmt.Fprint(out, "> ")
	if file, ok := out.(*os.File); ok {
		_ = file.Sync()
	}
}

type named struct {
	ID   string
	Name string
}

func bindContext(opt Options, keys input) (shellState, error) {
	var state shellState
	project, err := pick(opt, keys, "Project:", "Projects:", "/api/projects", "name", func(name string) (named, error) {
		return createProject(opt, name)
	})
	if err != nil {
		return state, err
	}
	state.projectID = project.ID
	state.projectName = project.Name
	thread, err := pick(opt, keys, "Thread:", "Threads:", "/api/projects/"+project.ID+"/threads", "title", func(name string) (named, error) {
		return createThread(opt, project.ID, name)
	})
	if err != nil {
		return state, err
	}
	state.threadID = thread.ID
	state.threadTitle = thread.Name
	return state, nil
}

func pick(opt Options, keys input, askLabel, listLabel, path, nameKey string, create func(string) (named, error)) (named, error) {
	raw, err := getBody(opt, path)
	if err != nil {
		return named{}, err
	}
	var rows []map[string]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		return named{}, err
	}
	items := make([]named, 0, len(rows))
	for _, row := range rows {
		items = append(items, named{ID: row["id"], Name: row[nameKey]})
	}
	switch len(items) {
	case 0:
		name, err := ask(opt, keys, askLabel)
		if err != nil {
			return named{}, err
		}
		return create(name)
	case 1:
		return items[0], nil
	default:
		fmt.Fprintln(opt.Out, listLabel)
		for i, item := range items {
			fmt.Fprintf(opt.Out, "%d. %s\n", i+1, item.Name)
		}
		choice, err := ask(opt, keys, "Select:")
		if err != nil {
			return named{}, err
		}
		var index int
		if _, err := fmt.Sscanf(choice, "%d", &index); err != nil || index < 1 || index > len(items) {
			return named{}, fmt.Errorf("无效选择")
		}
		return items[index-1], nil
	}
}

func ask(opt Options, keys input, label string) (string, error) {
	fmt.Fprintln(opt.Out, label)
	writePrompt(opt.Out)
	line, err := keys.line()
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("需要一个名字")
	}
	return line, nil
}

func createProject(opt Options, name string) (named, error) {
	raw, err := postBody(opt, "/api/projects", map[string]string{"name": name})
	if err != nil {
		return named{}, err
	}
	var item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return named{}, err
	}
	return named{ID: item.ID, Name: item.Name}, nil
}

func createThread(opt Options, projectID, title string) (named, error) {
	raw, err := postBody(opt, "/api/projects/"+projectID+"/threads", map[string]string{"title": title})
	if err != nil {
		return named{}, err
	}
	var item struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return named{}, err
	}
	return named{ID: item.ID, Name: item.Title}, nil
}

func formatMemory(raw []byte) string {
	var rows []struct {
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return strings.TrimSpace(string(raw))
	}
	if len(rows) == 0 {
		return "（无）"
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, row.Kind+": "+row.Content)
	}
	return strings.Join(lines, "\n")
}

func getBody(opt Options, path string) ([]byte, error) {
	response, err := http.Get(strings.TrimRight(opt.BaseURL, "/") + path)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if len(raw) == 0 {
		raw = []byte("[]")
	}
	return raw, nil
}

func postBody(opt Options, path string, body any) ([]byte, error) {
	rawBody, _ := json.Marshal(body)
	response, err := http.Post(strings.TrimRight(opt.BaseURL, "/")+path, "application/json", bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
