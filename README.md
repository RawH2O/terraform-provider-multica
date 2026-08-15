# Terraform Provider for Multica

This is the first provider slice for managing Multica agents as Terraform
resources. The resource accepts the `multica-declarative` agent YAML shape via
Terraform's `yamldecode`, then calls the Multica API directly.

```hcl
terraform {
  required_providers {
    multica = {
      source = "xiehengjian/multica"
    }
  }
}

provider "multica" {
  # These may also be supplied by MULTICA_SERVER_URL,
  # MULTICA_API_TOKEN and MULTICA_WORKSPACE_ID.
  server_url   = var.multica_server_url
  token        = var.multica_api_token
  workspace_id = var.multica_workspace_id
}

resource "multica_agent" "developer" {
  config = yamldecode(file("${path.root}/agents/developer/agent.yaml"))
}
```

The declaration can use the community format:

```yaml
name: Unity Developer
description: Implements Unity tasks.
instructions: Follow the repository conventions.

model:
  id: gpt-5.6

skills:
  - unity-development

multica:
  runtime: main-desktop
  runtimeConfig:
    sandbox: strict
  thinkingLevel: high
  maxConcurrentTasks: 1
  permission: private
  customArgs: []
```

The first slice implements create, read, update, archive-on-delete, import, runtime
resolution, skill binding, invocation permissions, custom environment files, MCP
configuration files, and the Composio allowlist. The provider uses the dedicated
`/env` endpoint for environment changes and never sends `custom_env` through the
generic agent update endpoint. A computed `content_hash` includes the declaration
and referenced file contents, so editing an instruction/env/MCP file participates in
the Terraform plan.

Secret-bearing values must use `customEnvFile` and `mcpConfigFile`. Inline
`customEnv` and `mcpConfig` are rejected so plaintext secrets are not copied into
Terraform state. File paths are currently resolved relative to Terraform's working
directory; use a root-relative path in the decoded YAML when the YAML file lives in
a nested agent directory.

`avatarFile` and `disabledRuntimeSkills` are recognized as community fields but are
explicitly rejected for now because the current provider slice has no faithful API
mutation for them. Skills must already exist in the workspace; creating skills is
the next resource slice.

## Local verification

```bash
gofmt -w .
go test ./...
go vet ./...
go build ./...
```
