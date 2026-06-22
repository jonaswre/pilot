package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultExecdPort = 44772

type SandboxState string

const (
	SandboxStatePending    SandboxState = "Pending"
	SandboxStateRunning    SandboxState = "Running"
	SandboxStateFailed     SandboxState = "Failed"
	SandboxStateTerminated SandboxState = "Terminated"
)

type Sandbox struct {
	ID     string        `json:"id"`
	Status SandboxStatus `json:"status"`
}

type SandboxStatus struct {
	State   SandboxState `json:"state"`
	Reason  string       `json:"reason,omitempty"`
	Message string       `json:"message,omitempty"`
}

type SandboxEndpoint struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type CommandSpec struct {
	Command string
	Args    []string
	CWD     string
	Timeout time.Duration
	Env     map[string]string
}

func (s CommandSpec) ShellCommand() string {
	if len(s.Args) == 0 {
		return s.Command
	}
	parts := []string{shellQuote(s.Command)}
	for _, arg := range s.Args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

type CommandEvent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Success  bool
	Events   []CommandEvent
}

type OpenSandboxClient struct {
	lifecycleBase string
	apiKey        string
	httpClient    *http.Client
}

func NewOpenSandboxClient(endpoint, apiKey string, httpClient *http.Client) *OpenSandboxClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	base := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return &OpenSandboxClient{
		lifecycleBase: base,
		apiKey:        apiKey,
		httpClient:    httpClient,
	}
}

func (c *OpenSandboxClient) CreateSandbox(ctx context.Context, payload map[string]any) (*Sandbox, error) {
	var sandbox Sandbox
	if err := c.doJSON(ctx, http.MethodPost, c.lifecycleBase+"/sandboxes", payload, http.StatusAccepted, &sandbox); err != nil {
		return nil, err
	}
	return &sandbox, nil
}

func (c *OpenSandboxClient) GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error) {
	var sandbox Sandbox
	if err := c.doJSON(ctx, http.MethodGet, c.lifecycleBase+"/sandboxes/"+url.PathEscape(sandboxID), nil, http.StatusOK, &sandbox); err != nil {
		return nil, err
	}
	return &sandbox, nil
}

func (c *OpenSandboxClient) WaitForRunning(ctx context.Context, sandboxID string, pollInterval time.Duration) (*Sandbox, error) {
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		sandbox, err := c.GetSandbox(ctx, sandboxID)
		if err != nil {
			return nil, err
		}
		switch sandbox.Status.State {
		case SandboxStateRunning:
			return sandbox, nil
		case SandboxStateFailed, SandboxStateTerminated:
			return nil, fmt.Errorf("sandbox %s reached terminal state %q: %s", sandboxID, sandbox.Status.State, sandbox.Status.Message)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *OpenSandboxClient) DeleteSandbox(ctx context.Context, sandboxID string) error {
	return c.doJSON(ctx, http.MethodDelete, c.lifecycleBase+"/sandboxes/"+url.PathEscape(sandboxID), nil, http.StatusNoContent, nil)
}

func (c *OpenSandboxClient) GetEndpoint(ctx context.Context, sandboxID string, port int) (*SandboxEndpoint, error) {
	endpointURL := fmt.Sprintf("%s/sandboxes/%s/endpoints/%d?use_server_proxy=true", c.lifecycleBase, url.PathEscape(sandboxID), port)
	var endpoint SandboxEndpoint
	if err := c.doJSON(ctx, http.MethodGet, endpointURL, nil, http.StatusOK, &endpoint); err != nil {
		return nil, err
	}
	if endpoint.Headers == nil {
		endpoint.Headers = map[string]string{}
	}
	return &endpoint, nil
}

func (c *OpenSandboxClient) RunCommand(ctx context.Context, execdEndpoint string, headers map[string]string, spec CommandSpec) (*CommandResult, error) {
	body := map[string]any{
		"command":    spec.ShellCommand(),
		"cwd":        spec.CWD,
		"background": false,
	}
	if spec.Timeout > 0 {
		body["timeout"] = spec.Timeout.Milliseconds()
	}
	if len(spec.Env) > 0 {
		body["envs"] = spec.Env
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal command request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(execdEndpoint, "/")+"/command", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, responseError("run command", resp)
	}
	return parseCommandSSE(resp.Body)
}

func (c *OpenSandboxClient) doJSON(ctx context.Context, method, requestURL string, body any, wantStatus int, dst any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("OPEN-SANDBOX-API-KEY", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return responseError(method+" "+requestURL, resp)
	}
	if dst == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func parseCommandSSE(r io.Reader) (*CommandResult, error) {
	result := &CommandResult{ExitCode: -1}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event CommandEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, fmt.Errorf("decode command event: %w", err)
		}
		result.Events = append(result.Events, event)
		switch event.Type {
		case "stdout":
			result.Stdout += event.Text
		case "stderr", "error":
			result.Stderr += event.Text
		case "result":
			if event.ExitCode != nil {
				result.ExitCode = *event.ExitCode
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	result.Success = result.ExitCode == 0
	return result, nil
}

func responseError(operation string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s: unexpected status %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(body)))
}
