package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

func TestDecodeDeclarativeAgentConfig(t *testing.T) {
	configValue := goToDynamic(map[string]any{
		"name":         "Unity Developer",
		"description":  "Builds Unity features",
		"instructions": "Follow the repository conventions.",
		"model":        map[string]any{"id": "gpt-5.6"},
		"skills": []any{
			"unity-development",
			map[string]any{"name": "optional-review-checks", "enabled": false},
		},
		"multica": map[string]any{
			"runtime":            "main-desktop",
			"runtimeConfig":      map[string]any{"sandbox": "strict"},
			"thinkingLevel":      "high",
			"maxConcurrentTasks": int64(1),
			"customArgs":         []string{"--full-auto"},
			"permission": map[string]any{
				"mode":      "public_to",
				"workspace": true,
				"members":   []string{"member-1"},
			},
		},
	})

	config, err := decodeDeclarativeConfig(context.Background(), configValue)
	if err != nil {
		t.Fatalf("decodeDeclarativeConfig() error = %v", err)
	}
	if got := config.Name.ValueString(); got != "Unity Developer" {
		t.Fatalf("name = %q", got)
	}
	if got := config.Runtime.Reference.ValueString(); got != "main-desktop" {
		t.Fatalf("runtime reference = %q", got)
	}
	if got := config.Model.ValueString(); got != "gpt-5.6" {
		t.Fatalf("model = %q", got)
	}
	if got := config.PermissionMode.ValueString(); got != "public_to" {
		t.Fatalf("permission mode = %q", got)
	}
	var targets []invocationTargetModel
	if diags := config.InvocationTargets.ElementsAs(context.Background(), &targets, false); diags.HasError() {
		t.Fatalf("invocation target diagnostics = %v", diags)
	}
	if len(targets) != 2 {
		t.Fatalf("invocation targets = %+v", targets)
	}
}

func TestDeclarativeFilesAreLoadedRelativeToWorkingDirectory(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "AGENT.md"), []byte("instructions from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "custom-env.json"), []byte(`{"TOKEN":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	configValue := goToDynamic(map[string]any{
		"name":             "file-backed",
		"instructionsFile": "AGENT.md",
		"multica": map[string]any{
			"runtime":       map[string]any{"id": "runtime-1"},
			"customEnvFile": "custom-env.json",
		},
	})
	config, err := decodeDeclarativeConfig(context.Background(), configValue)
	if err != nil {
		t.Fatalf("decodeDeclarativeConfig() error = %v", err)
	}
	if config.Instructions.ValueString() != "instructions from file" {
		t.Fatalf("instructions = %q", config.Instructions.ValueString())
	}
	var env map[string]string
	if diags := config.CustomEnv.ElementsAs(context.Background(), &env, false); diags.HasError() {
		t.Fatalf("custom env diagnostics = %v", diags)
	}
	if env["TOKEN"] != "secret" {
		t.Fatalf("custom env = %+v", env)
	}
	firstHash, err := declarativeContentHash(configValue)
	if err != nil {
		t.Fatalf("declarativeContentHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "AGENT.md"), []byte("changed instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondHash, err := declarativeContentHash(configValue)
	if err != nil {
		t.Fatalf("declarativeContentHash() after change error = %v", err)
	}
	if firstHash == secondHash {
		t.Fatalf("content hash did not change after referenced file changed: %s", firstHash)
	}
}

func TestRequestBodyResolvesDeclarativeRuntimeAndSkills(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/runtimes":
			_, _ = w.Write([]byte(`[{"id":"runtime-1","name":"main-desktop","provider":"codex"}]`))
		case "/api/skills":
			_, _ = w.Write([]byte(`[{"id":"skill-1","name":"unity-development"}]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	config := agentConfigModel{
		Name:          types.StringValue("Unity Developer"),
		Runtime:       runtimeSelectorModel{Reference: types.StringValue("main-desktop")},
		RuntimeConfig: types.StringValue(`{"sandbox":"strict"}`),
		Skills:        stringSetValue([]string{"unity-development"}),
	}
	body, skillIDs, err := (&agentResource{client: client.New(server.URL, "token", "workspace", nil)}).requestBody(context.Background(), config)
	if err != nil {
		t.Fatalf("requestBody() error = %v", err)
	}
	if body["runtime_id"] != "runtime-1" {
		t.Fatalf("runtime_id = %v", body["runtime_id"])
	}
	if len(skillIDs) != 1 || skillIDs[0] != "skill-1" {
		t.Fatalf("skill IDs = %v", skillIDs)
	}
	encoded, err := json.Marshal(body["runtime_config"])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"sandbox":"strict"}` {
		t.Fatalf("runtime_config = %s", encoded)
	}
}
