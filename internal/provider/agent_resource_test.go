package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

func TestIsArchivedAgentError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "missing agent",
			err:  &client.HTTPError{StatusCode: http.StatusNotFound},
			want: true,
		},
		{
			name: "already archived conflict",
			err: &client.HTTPError{
				StatusCode: http.StatusConflict,
				Body:       `{"error":"agent is already archived"}`,
			},
			want: true,
		},
		{
			name: "other conflict",
			err: &client.HTTPError{
				StatusCode: http.StatusConflict,
				Body:       `{"error":"agent has active tasks"}`,
			},
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("network unavailable"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isArchivedAgentError(tt.err); got != tt.want {
				t.Fatalf("isArchivedAgentError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveSkillsDoesNotImportMissingRemoteSkill(t *testing.T) {
	var importCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/skills":
			_ = json.NewEncoder(w).Encode([]client.Skill{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/skills/import":
			importCalls++
			http.Error(w, "unexpected import", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resource := &agentResource{client: client.New(server.URL, "token", "workspace", nil)}
	values, diags := types.SetValue(types.StringType, []attr.Value{
		types.StringValue("https://github.com/acme/review-helper"),
	})
	if diags.HasError() {
		t.Fatalf("types.SetValue() diagnostics = %v", diags)
	}
	_, err := resource.resolveSkills(context.Background(), values)
	if err == nil || !strings.Contains(err.Error(), "import it locally before running Terraform") {
		t.Fatalf("resolveSkills() error = %v, want local-import guidance", err)
	}
	if importCalls != 0 {
		t.Fatalf("resolveSkills() made %d import calls, want 0", importCalls)
	}
}
