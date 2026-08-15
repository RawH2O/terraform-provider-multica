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
