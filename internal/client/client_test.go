package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListRuntimesDecodesResponseAndScopesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtimes" {
			t.Fatalf("path = %s, want /api/runtimes", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "workspace" {
			t.Fatalf("workspace header = %q, want workspace", got)
		}
		if got := r.Header.Get("X-Client-Platform"); got != "terraform-provider-multica" {
			t.Fatalf("client platform = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Runtime{{ID: "runtime-1", Name: "main-desktop"}})
	}))
	defer server.Close()

	runtimes, err := New(server.URL, "token", "workspace", nil).ListRuntimes(context.Background())
	if err != nil {
		t.Fatalf("ListRuntimes() error = %v", err)
	}
	if len(runtimes) != 1 || runtimes[0].ID != "runtime-1" {
		t.Fatalf("runtimes = %+v", runtimes)
	}
}

func TestHookCRUDAndAgentBindingUseHookEndpoints(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/api/hooks" {
				t.Fatalf("request = %s %s, want GET /api/hooks", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(`[{"id":"hook-1","name":"require-mention","events":["Stop"]}]`))
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/api/hooks" {
				t.Fatalf("request = %s %s, want POST /api/hooks", r.Method, r.URL.Path)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if body["name"] != "require-mention" || body["command"] != "agent-hook-kit" {
				t.Fatalf("create body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"id":"hook-1","name":"require-mention","command":"agent-hook-kit","events":["Stop"]}`))
		case 3:
			if r.Method != http.MethodPut || r.URL.Path != "/api/hooks/hook-1" {
				t.Fatalf("request = %s %s, want PUT /api/hooks/hook-1", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"hook-1","name":"require-mention-v2","command":"agent-hook-kit","events":["Stop"]}`))
		case 4:
			if r.Method != http.MethodDelete || r.URL.Path != "/api/hooks/hook-1" {
				t.Fatalf("request = %s %s, want DELETE /api/hooks/hook-1", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		case 5:
			if r.Method != http.MethodPut || r.URL.Path != "/api/agents/agent-1/hooks" {
				t.Fatalf("request = %s %s, want PUT /api/agents/agent-1/hooks", r.Method, r.URL.Path)
			}
			var body map[string][]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode binding body: %v", err)
			}
			if len(body["hook_ids"]) != 1 || body["hook_ids"][0] != "hook-1" {
				t.Fatalf("binding body = %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "token", "workspace", nil)
	hooks, err := c.ListHooks(context.Background())
	if err != nil || len(hooks) != 1 || hooks[0].ID != "hook-1" {
		t.Fatalf("ListHooks() = %#v, error = %v", hooks, err)
	}
	created, err := c.CreateHook(context.Background(), map[string]any{
		"name": "require-mention", "command": "agent-hook-kit", "events": []string{"Stop"},
	})
	if err != nil || created.ID != "hook-1" {
		t.Fatalf("CreateHook() = %#v, error = %v", created, err)
	}
	updated, err := c.UpdateHook(context.Background(), "hook-1", map[string]any{"name": "require-mention-v2"})
	if err != nil || updated.Name != "require-mention-v2" {
		t.Fatalf("UpdateHook() = %#v, error = %v", updated, err)
	}
	if err := c.DeleteHook(context.Background(), "hook-1"); err != nil {
		t.Fatalf("DeleteHook() error = %v", err)
	}
	if err := c.SetAgentHooks(context.Background(), "agent-1", []string{"hook-1"}); err != nil {
		t.Fatalf("SetAgentHooks() error = %v", err)
	}
}

func TestGetAgentReturnsStructuredHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "token", "workspace", nil).GetAgent(context.Background(), "agent-1")
	if err == nil {
		t.Fatal("GetAgent() error = nil, want error")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("error type = %T, want *HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusNotFound || !strings.Contains(httpErr.Body, "not found") {
		t.Fatalf("HTTP error = %+v", httpErr)
	}
}

func TestGetAgentFallsBackToCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/agent-1":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		case "/api/agents":
			if r.URL.Query().Get("include_archived") != "true" {
				t.Fatalf("include_archived = %q, want true", r.URL.Query().Get("include_archived"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"agent-1","name":"archived-agent","archived_at":"2026-01-01T00:00:00Z"}]`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	agent, err := New(server.URL, "token", "workspace", nil).GetAgent(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("GetAgent() error = %v", err)
	}
	if agent.Name != "archived-agent" || agent.ArchivedAt == nil {
		t.Fatalf("agent = %+v", agent)
	}
}

func TestGetAutopilotFallsBackToCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/autopilots/autopilot-1":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		case "/api/autopilots":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"autopilots":[{"id":"autopilot-1","title":"legacy"}]}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	autopilot, err := New(server.URL, "token", "workspace", nil).GetAutopilot(context.Background(), "autopilot-1")
	if err != nil {
		t.Fatalf("GetAutopilot() error = %v", err)
	}
	if autopilot["title"] != "legacy" {
		t.Fatalf("autopilot = %#v", autopilot)
	}
}

func TestImportSkillSendsConflictPolicyAndDecodesResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/skills/import" {
			t.Fatalf("request = %s %s, want POST /api/skills/import", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["url"] != "https://skills.sh/acme/review" || body["on_conflict"] != "skip" {
			t.Fatalf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SkillImportResult{
			Status: "skipped",
			ExistingSkill: &struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}{ID: "skill-1", Name: "review"},
		})
	}))
	defer server.Close()

	result, err := New(server.URL, "token", "workspace", nil).ImportSkill(context.Background(), "https://skills.sh/acme/review", "skip")
	if err != nil {
		t.Fatalf("ImportSkill() error = %v", err)
	}
	if result.Status != "skipped" || result.ExistingSkill == nil || result.ExistingSkill.ID != "skill-1" {
		t.Fatalf("result = %+v", result)
	}
}

func TestDeleteJSONWithBodySendsJSONPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/squads/squad-1/members" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["member_type"] != "agent" || body["member_id"] != "agent-1" {
			t.Fatalf("body = %#v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := New(server.URL, "token", "workspace", nil).DeleteJSONWithBody(context.Background(), "/api/squads/squad-1/members", map[string]string{
		"member_type": "agent",
		"member_id":   "agent-1",
	})
	if err != nil {
		t.Fatalf("DeleteJSONWithBody() error = %v", err)
	}
}

func TestPublishPluginPackageUsesWorkspaceMultipartEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/workspaces/workspace/plugins/packages" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		file, _, err := r.FormFile("bundle")
		if err != nil {
			t.Fatalf("bundle form file: %v", err)
		}
		defer file.Close()
		var got []byte
		got, err = io.ReadAll(file)
		if err != nil {
			t.Fatalf("read bundle: %v", err)
		}
		if string(got) != "zip-bytes" {
			t.Fatalf("bundle = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"package-1","plugin_key":"com.example.relay","name":"Relay","versions":[{"id":"version-1","version":"1.0.0"}]}`))
	}))
	defer server.Close()

	packageSummary, err := New(server.URL, "token", "workspace", nil).PublishPluginPackage(context.Background(), []byte("zip-bytes"))
	if err != nil {
		t.Fatalf("PublishPluginPackage() error = %v", err)
	}
	if packageSummary.ID != "package-1" || len(packageSummary.Versions) != 1 {
		t.Fatalf("package summary = %+v", packageSummary)
	}
}
