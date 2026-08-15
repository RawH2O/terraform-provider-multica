package client

import (
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

// Client is the small, workspace-scoped HTTP client used by the provider.
// It intentionally talks to the Multica API instead of shelling out to the
// multica CLI so Terraform owns state and plan semantics.
type Client struct {
	BaseURL     string
	Token       string
	WorkspaceID string
	HTTPClient  *http.Client
}

type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

func New(baseURL, token, workspaceID string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Token:       token,
		WorkspaceID: workspaceID,
		HTTPClient:  httpClient,
	}
}

type Runtime struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CustomName  string `json:"custom_name"`
	Provider    string `json:"provider"`
	WorkspaceID string `json:"workspace_id"`
}

type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InvocationTarget struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id,omitempty"`
}

type Agent struct {
	ID                        string             `json:"id"`
	WorkspaceID               string             `json:"workspace_id"`
	RuntimeID                 string             `json:"runtime_id"`
	Name                      string             `json:"name"`
	Description               string             `json:"description"`
	Instructions              string             `json:"instructions"`
	AvatarURL                 *string            `json:"avatar_url"`
	RuntimeMode               string             `json:"runtime_mode"`
	RuntimeConfig             any                `json:"runtime_config"`
	CustomArgs                []string           `json:"custom_args"`
	MCPConfig                 json.RawMessage    `json:"mcp_config"`
	MCPConfigRedacted         bool               `json:"mcp_config_redacted"`
	HasCustomEnv              bool               `json:"has_custom_env"`
	CustomEnvKeyCount         int                `json:"custom_env_key_count"`
	Visibility                string             `json:"visibility"`
	PermissionMode            string             `json:"permission_mode"`
	InvocationTargets         []InvocationTarget `json:"invocation_targets"`
	Status                    string             `json:"status"`
	MaxConcurrentTasks        int32              `json:"max_concurrent_tasks"`
	Model                     string             `json:"model"`
	ThinkingLevel             string             `json:"thinking_level"`
	ComposioAllowlist         []string           `json:"composio_toolkit_allowlist"`
	ComposioAllowlistRedacted bool               `json:"composio_toolkit_allowlist_redacted"`
	Skills                    []Skill            `json:"skills"`
	ArchivedAt                *string            `json:"archived_at"`
}

func (c *Client) ListRuntimes(ctx context.Context) ([]Runtime, error) {
	var result []Runtime
	err := c.get(ctx, "/api/runtimes", &result)
	return result, err
}

func (c *Client) ListSkills(ctx context.Context) ([]Skill, error) {
	var result []Skill
	err := c.get(ctx, "/api/skills", &result)
	return result, err
}

func (c *Client) GetAgent(ctx context.Context, id string) (Agent, error) {
	var result Agent
	err := c.get(ctx, "/api/agents/"+url.PathEscape(id), &result)
	return result, err
}

func (c *Client) CreateAgent(ctx context.Context, body map[string]any) (Agent, error) {
	var result Agent
	err := c.post(ctx, "/api/agents", body, &result)
	return result, err
}

func (c *Client) UpdateAgent(ctx context.Context, id string, body map[string]any) (Agent, error) {
	var result Agent
	err := c.put(ctx, "/api/agents/"+url.PathEscape(id), body, &result)
	return result, err
}

func (c *Client) ArchiveAgent(ctx context.Context, id string) error {
	return c.post(ctx, "/api/agents/"+url.PathEscape(id)+"/archive", nil, nil)
}

func (c *Client) RestoreAgent(ctx context.Context, id string) error {
	return c.post(ctx, "/api/agents/"+url.PathEscape(id)+"/restore", nil, nil)
}

func (c *Client) SetAgentSkills(ctx context.Context, id string, skillIDs []string) error {
	return c.put(ctx, "/api/agents/"+url.PathEscape(id)+"/skills", map[string]any{
		"skill_ids": skillIDs,
	}, nil)
}

func (c *Client) SetAgentEnv(ctx context.Context, id string, env map[string]string) error {
	return c.put(ctx, "/api/agents/"+url.PathEscape(id)+"/env", map[string]any{
		"custom_env": env,
	}, nil)
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.WorkspaceID)
	}
	req.Header.Set("X-Client-Platform", "terraform-provider-multica")

	return c.HTTPClient.Do(req)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) put(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(data)}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
