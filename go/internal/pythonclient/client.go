package pythonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type RunBody struct {
	ProjectID     string `json:"project_id"`
	ThreadID      string `json:"thread_id"`
	RunID         string `json:"run_id"`
	Goal          string `json:"goal"`
	WorkspaceRoot string `json:"workspace_root"`
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c Client) Start(ctx context.Context, body RunBody) error {
	return c.postAccepted(ctx, "/internal/runs", body)
}

func (c Client) Resume(ctx context.Context, body RunBody) error {
	return c.postAccepted(ctx, "/internal/runs/"+body.RunID+"/resume", body)
}

func (c Client) Cancel(ctx context.Context, runID string) error {
	return c.postAccepted(ctx, "/internal/runs/"+runID+"/cancel", map[string]string{})
}

func (c Client) ListMemory(ctx context.Context, projectID string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/internal/projects/"+projectID+"/memory", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("python memory %d", response.StatusCode)
	}
	return raw, nil
}

func (c Client) postAccepted(ctx context.Context, path string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("python %d %s", response.StatusCode, payload)
	}
	return nil
}
