package client

import (
	"context"
	"encoding/json"
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
