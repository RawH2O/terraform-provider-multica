package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
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
	ID          string      `json:"id"`
	WorkspaceID string      `json:"workspace_id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Content     string      `json:"content"`
	Config      any         `json:"config"`
	Files       []SkillFile `json:"files,omitempty"`
}

type SkillFile struct {
	ID        string `json:"id"`
	SkillID   string `json:"skill_id"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type PluginPackage struct {
	ID        string                 `json:"id"`
	PluginKey string                 `json:"plugin_key"`
	Name      string                 `json:"name"`
	Versions  []PluginPackageVersion `json:"versions"`
	CreatedAt string                 `json:"created_at"`
}

type PluginPackageVersion struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
	SizeBytes   int64  `json:"size_bytes"`
	PublishedAt string `json:"published_at"`
	Installed   bool   `json:"installed"`
}

type PluginInstallation struct {
	ID                string         `json:"id"`
	PluginKey         string         `json:"plugin_key"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Version           string         `json:"version"`
	PackageVersionID  string         `json:"package_version_id"`
	Enabled           bool           `json:"enabled"`
	GrantedScopes     []string       `json:"granted_scopes"`
	Config            map[string]any `json:"config"`
	ConfiguredSecrets []string       `json:"configured_secrets"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
}

type SkillImportResult struct {
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	Skill         *Skill `json:"skill,omitempty"`
	ExistingSkill *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"existing_skill,omitempty"`
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

func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	var result []Agent
	err := c.get(ctx, "/api/agents?include_archived=true", &result)
	return result, err
}

func (c *Client) ListSkills(ctx context.Context) ([]Skill, error) {
	var result []Skill
	err := c.get(ctx, "/api/skills", &result)
	return result, err
}

func (c *Client) GetSkill(ctx context.Context, id string) (Skill, error) {
	var result Skill
	err := c.get(ctx, "/api/skills/"+url.PathEscape(id), &result)
	return result, err
}

func (c *Client) CreateSkill(ctx context.Context, body map[string]any) (Skill, error) {
	var result Skill
	err := c.post(ctx, "/api/skills", body, &result)
	return result, err
}

func (c *Client) UpdateSkill(ctx context.Context, id string, body map[string]any) (Skill, error) {
	var result Skill
	err := c.put(ctx, "/api/skills/"+url.PathEscape(id), body, &result)
	return result, err
}

func (c *Client) DeleteSkill(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/skills/"+url.PathEscape(id))
}

func (c *Client) ImportSkill(ctx context.Context, sourceURL, onConflict string) (SkillImportResult, error) {
	var result SkillImportResult
	err := c.post(ctx, "/api/skills/import", map[string]any{
		"url":         sourceURL,
		"on_conflict": onConflict,
	}, &result)
	return result, err
}

func (c *Client) ListSkillFiles(ctx context.Context, skillID string) ([]SkillFile, error) {
	var result []SkillFile
	err := c.get(ctx, "/api/skills/"+url.PathEscape(skillID)+"/files", &result)
	return result, err
}

func (c *Client) UpsertSkillFile(ctx context.Context, skillID string, path, content string) (SkillFile, error) {
	var result SkillFile
	err := c.put(ctx, "/api/skills/"+url.PathEscape(skillID)+"/files", map[string]any{
		"path": path, "content": content,
	}, &result)
	return result, err
}

func (c *Client) DeleteSkillFile(ctx context.Context, skillID, fileID string) error {
	return c.delete(ctx, "/api/skills/"+url.PathEscape(skillID)+"/files/"+url.PathEscape(fileID))
}

// The squad and autopilot APIs have evolved additively and expose a few
// server-computed fields. Keep their provider transport deliberately generic
// so the provider can preserve declarative config without coupling the client
// to every UI-only response field.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.get(ctx, path, out)
}

func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	return c.post(ctx, path, body, out)
}

func (c *Client) PatchJSON(ctx context.Context, path string, body, out any) error {
	return c.patch(ctx, path, body, out)
}

func (c *Client) PutJSON(ctx context.Context, path string, body, out any) error {
	return c.put(ctx, path, body, out)
}

func (c *Client) DeleteJSON(ctx context.Context, path string) error {
	return c.delete(ctx, path)
}

func (c *Client) DeleteJSONWithBody(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodDelete, path, body, nil)
}

func (c *Client) GetAgent(ctx context.Context, id string) (Agent, error) {
	var result Agent
	err := c.get(ctx, "/api/agents/"+url.PathEscape(id), &result)
	if err == nil {
		return result, nil
	}

	// The single-agent endpoint hides archived agents, while the collection
	// endpoint can include them. Keep imported archived agents readable so a
	// one-time Terraform import does not immediately lose them from state.
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		return result, err
	}
	agents, listErr := c.ListAgents(ctx)
	if listErr != nil {
		return result, err
	}
	for _, agent := range agents {
		if agent.ID == id {
			return agent, nil
		}
	}
	return result, err
}

func (c *Client) GetAutopilot(ctx context.Context, id string) (map[string]any, error) {
	var result map[string]any
	err := c.get(ctx, "/api/autopilots/"+url.PathEscape(id), &result)
	if err == nil {
		return result, nil
	}

	// Paused or legacy autopilots may be omitted by the detail endpoint while
	// remaining present in the collection response. Preserve them during state
	// refresh so a one-time Terraform import can still produce a destroy plan
	// when the declaration is later removed from Git.
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		return result, err
	}
	var collection struct {
		Autopilots []map[string]any `json:"autopilots"`
	}
	if listErr := c.get(ctx, "/api/autopilots", &collection); listErr != nil {
		return result, err
	}
	for _, autopilot := range collection.Autopilots {
		if value, ok := autopilot["id"].(string); ok && value == id {
			return autopilot, nil
		}
	}
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

func (c *Client) ListPluginPackages(ctx context.Context) ([]PluginPackage, error) {
	var result struct {
		Packages []PluginPackage `json:"packages"`
	}
	if err := c.get(ctx, "/api/workspaces/"+url.PathEscape(c.WorkspaceID)+"/plugins/packages", &result); err != nil {
		return nil, err
	}
	return result.Packages, nil
}

func (c *Client) PublishPluginPackage(ctx context.Context, archive []byte) (PluginPackage, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "multica.plugin.zip")
	if err != nil {
		return PluginPackage{}, err
	}
	if _, err := part.Write(archive); err != nil {
		return PluginPackage{}, err
	}
	if err := writer.Close(); err != nil {
		return PluginPackage{}, err
	}

	path := "/api/workspaces/" + url.PathEscape(c.WorkspaceID) + "/plugins/packages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, &body)
	if err != nil {
		return PluginPackage{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return PluginPackage{}, err
	}
	defer resp.Body.Close()

	var result PluginPackage
	if err := decodeResponse(resp, http.MethodPost, path, &result); err != nil {
		return PluginPackage{}, err
	}
	return result, nil
}

func (c *Client) ListPluginInstallations(ctx context.Context) ([]PluginInstallation, error) {
	var result struct {
		Plugins []PluginInstallation `json:"plugins"`
	}
	if err := c.get(ctx, "/api/workspaces/"+url.PathEscape(c.WorkspaceID)+"/plugins", &result); err != nil {
		return nil, err
	}
	return result.Plugins, nil
}

func (c *Client) InstallPlugin(ctx context.Context, versionID string, grantedScopes []string) (PluginInstallation, error) {
	var result PluginInstallation
	path := "/api/workspaces/" + url.PathEscape(c.WorkspaceID) + "/plugins"
	err := c.post(ctx, path, map[string]any{
		"version_id":     versionID,
		"granted_scopes": grantedScopes,
	}, &result)
	return result, err
}

func (c *Client) ConfigurePlugin(ctx context.Context, installationID string, values map[string]any) (PluginInstallation, error) {
	var result PluginInstallation
	path := "/api/workspaces/" + url.PathEscape(c.WorkspaceID) + "/plugins/" + url.PathEscape(installationID) + "/config"
	err := c.put(ctx, path, map[string]any{"values": values}, &result)
	return result, err
}

func (c *Client) SetPluginEnabled(ctx context.Context, installationID string, enabled bool) (PluginInstallation, error) {
	var result PluginInstallation
	action := "disable"
	if enabled {
		action = "enable"
	}
	path := "/api/workspaces/" + url.PathEscape(c.WorkspaceID) + "/plugins/" + url.PathEscape(installationID) + "/" + action
	err := c.post(ctx, path, nil, &result)
	return result, err
}

func (c *Client) UninstallPlugin(ctx context.Context, installationID string) error {
	path := "/api/workspaces/" + url.PathEscape(c.WorkspaceID) + "/plugins/" + url.PathEscape(installationID)
	return c.delete(ctx, path)
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
	c.setHeaders(req)

	return c.HTTPClient.Do(req)
}

func (c *Client) setHeaders(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.WorkspaceID)
	}
	req.Header.Set("X-Client-Platform", "terraform-provider-multica")
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

func (c *Client) patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, body, out)
}

func (c *Client) delete(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeResponse(resp, method, path, out)
}

func decodeResponse(resp *http.Response, method, path string, out any) error {
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(data)}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
