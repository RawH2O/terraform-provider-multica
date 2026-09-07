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
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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

func TestManagedResourceIDsReusePriorStateDuringPlan(t *testing.T) {
	resources := []struct {
		name   string
		impl   resource.Resource
		schema func(context.Context, resource.SchemaRequest, *resource.SchemaResponse)
	}{
		{name: "agent", impl: &agentResource{}, schema: (&agentResource{}).Schema},
		{name: "skill", impl: &skillResource{}, schema: (&skillResource{}).Schema},
		{name: "hook", impl: &hookResource{}, schema: (&hookResource{}).Schema},
		{name: "squad", impl: &squadResource{}, schema: (&squadResource{}).Schema},
		{name: "autopilot", impl: &autopilotResource{}, schema: (&autopilotResource{}).Schema},
		{name: "plugin", impl: &pluginResource{}, schema: (&pluginResource{}).Schema},
	}

	for _, test := range resources {
		t.Run(test.name, func(t *testing.T) {
			response := resource.SchemaResponse{}
			test.schema(context.Background(), resource.SchemaRequest{}, &response)
			if response.Diagnostics.HasError() {
				t.Fatalf("schema diagnostics = %v", response.Diagnostics)
			}
			id, ok := response.Schema.Attributes["id"].(schema.StringAttribute)
			if !ok {
				t.Fatalf("id schema has type %T, want schema.StringAttribute", response.Schema.Attributes["id"])
			}
			if len(id.PlanModifiers) != 1 {
				t.Fatalf("id plan modifiers = %d, want one state-preserving modifier", len(id.PlanModifiers))
			}
		})
	}
}

func TestAgentArchiveStateConflictsAreIdempotent(t *testing.T) {
	if !isAlreadyArchivedAgentError(&client.HTTPError{
		StatusCode: http.StatusConflict,
		Body:       `{"error":"agent is already archived"}`,
	}) {
		t.Fatal("already archived conflict should be idempotent")
	}
	if !isNotArchivedAgentError(&client.HTTPError{
		StatusCode: http.StatusConflict,
		Body:       `{"error":"agent is not archived"}`,
	}) {
		t.Fatal("not archived conflict should be idempotent")
	}
	if isNotArchivedAgentError(&client.HTTPError{
		StatusCode: http.StatusConflict,
		Body:       `{"error":"agent is already archived"}`,
	}) {
		t.Fatal("already archived conflict should not match restore state")
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

func TestResolveSkillsMatchesShorthandRemoteURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/skills" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]client.Skill{{
			ID:   "skill-1",
			Name: "serenity-skill",
			Config: map[string]any{
				"origin": map[string]any{
					"source_url": "https://github.com/xiehengjian/serenity-skill",
				},
			},
		}})
	}))
	defer server.Close()

	resource := &agentResource{client: client.New(server.URL, "token", "workspace", nil)}
	values, diags := types.SetValue(types.StringType, []attr.Value{
		types.StringValue("github.com/xiehengjian/serenity-skill"),
	})
	if diags.HasError() {
		t.Fatalf("types.SetValue() diagnostics = %v", diags)
	}
	ids, err := resource.resolveSkills(context.Background(), values)
	if err != nil {
		t.Fatalf("resolveSkills() error = %v", err)
	}
	if len(ids) != 1 || ids[0] != "skill-1" {
		t.Fatalf("resolveSkills() IDs = %#v, want [skill-1]", ids)
	}
}

func TestResolveHooksMatchesName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/hooks" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]client.Hook{{ID: "hook-1", Name: "require-mention"}})
	}))
	defer server.Close()

	resource := &agentResource{client: client.New(server.URL, "token", "workspace", nil)}
	values, diags := types.SetValue(types.StringType, []attr.Value{types.StringValue("require-mention")})
	if diags.HasError() {
		t.Fatalf("types.SetValue() diagnostics = %v", diags)
	}
	ids, err := resource.resolveHooks(context.Background(), values)
	if err != nil {
		t.Fatalf("resolveHooks() error = %v", err)
	}
	if len(ids) != 1 || ids[0] != "hook-1" {
		t.Fatalf("resolveHooks() IDs = %#v, want [hook-1]", ids)
	}
}
