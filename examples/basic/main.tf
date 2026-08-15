terraform {
  required_providers {
    multica = {
      source = "xiehengjian/multica"
    }
  }
}

variable "multica_server_url" {
  type = string
}

variable "multica_api_token" {
  type      = string
  sensitive = true
}

variable "multica_workspace_id" {
  type = string
}

provider "multica" {
  server_url   = var.multica_server_url
  token        = var.multica_api_token
  workspace_id = var.multica_workspace_id
}

resource "multica_agent" "developer" {
  config = yamldecode(file("${path.root}/agent.yaml"))
}
