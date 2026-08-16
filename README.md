# Terraform Provider for Multica

This provider manages Multica configuration through Terraform while keeping
the YAML shape used by `multica-declarative` and the existing GitOps
repository. Terraform owns the plan, diff, state, and apply workflow; the
provider only translates resources to the Multica API.

## Provider configuration

```hcl
terraform {
  required_providers {
    multica = {
      source = "xiehengjian/multica"
    }
  }
}

provider "multica" {
  server_url   = var.multica_server_url
  token        = var.multica_api_token
  workspace_id = var.multica_workspace_id
}
```

The three provider arguments can also be supplied with
`MULTICA_SERVER_URL`, `MULTICA_API_TOKEN`, and `MULTICA_WORKSPACE_ID`.

## Supported resources

### `multica_agent`

The agent resource accepts the declarative agent object directly:

```hcl
resource "multica_agent" "developer" {
  config = yamldecode(file("${path.root}/agents/developer/agent.yaml"))
}
```

The following existing fields are translated to the agent API: runtime
selectors, model, instructions or `instructionsFile`, skills, runtime config,
permissions, custom arguments, MCP configuration files, custom environment
files, Composio allowlists, and archive state. A remote skill URL in `skills`
is imported once with conflict policy `skip` and then attached by ID.

### `multica_skill`

Local skills, including supporting files, can be managed in Terraform:

```hcl
resource "multica_skill" "review" {
  name        = "review"
  description = "Review changes before merge."
  content     = file("${path.root}/skills/review/SKILL.md")

  files = [{
    path    = "references/checklist.md"
    content = file("${path.root}/skills/review/references/checklist.md")
  }]
}
```

For a community skill, use `source_url` instead of `content`; create imports
it and update refreshes it with conflict policy `overwrite`:

```hcl
resource "multica_skill" "github_review" {
  name       = "github-review"
  source_url = "https://github.com/example/skills/tree/main/github-review"
}
```

`SKILL.md` is represented by `content` and must not be repeated in `files`.
The resource also preserves API `config`, including the import origin.

### `multica_squad`

Squads use the declarative YAML object as the Terraform value:

```hcl
resource "multica_squad" "research" {
  config = yamldecode(file("${path.root}/squads/research-team/squad.yaml"))
}
```

Supported fields include `name`, `leader`, `purpose`/`description`,
`instructions` or `description_file`, `avatar_url`, `status`, and agent/member
membership with roles. Agent references may be either IDs or names. `DELETE`
follows the Multica squad API and archives the squad.

### `multica_autopilot`

Autopilots and their triggers are managed as one declarative object:

```hcl
resource "multica_autopilot" "market_close" {
  config = yamldecode(file("${path.root}/autopilots/market-close/autopilot.yaml"))
}
```

Supported fields include `title`, `agent`/`assignee`, `mode`, `description` or
`description_file`, `project`/`project_id`, `status`,
`issue_title_template`, subscribers, and schedule/webhook triggers. A project
or agent can be written as its UUID or its workspace name. Trigger `label` is
used as the stable identity during updates; unlabeled triggers use kind,
cron, and timezone.

The current Multica autopilot API does not persist the old declarative
`priority` field. Values other than `none` are rejected rather than silently
ignored. Squad member `responsibility` and display-only fields are preserved
in Git but are not sent to an API endpoint that can persist them yet.

## State and secrets

The agent, squad, and autopilot resources expose a computed `content_hash`;
changing YAML or a referenced file therefore participates in the Terraform
plan. Use relative paths from the Terraform working directory for
`instructionsFile`,
`description_file`, `customEnvFile`, and `mcpConfigFile`.

Secret-bearing agent values must use `customEnvFile` and `mcpConfigFile`.
Inline `customEnv` and `mcpConfig` are rejected so plaintext secrets do not
enter Terraform state.

Import an existing API object with its UUID, then let the first refresh
populate the dynamic config:

```bash
terraform import multica_skill.review <skill-uuid>
terraform import multica_squad.research <squad-uuid>
terraform import multica_autopilot.market_close <autopilot-uuid>
```

## Local verification

```bash
gofmt -w .
go test ./...
go vet ./...
go build ./...
```
