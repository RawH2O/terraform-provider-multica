package main

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/xiehengjian/terraform-provider-multica/internal/provider"
)

func main() {
	providerserver.Serve(context.Background(), provider.New, providerserver.ServeOpts{
		Address: "registry.terraform.io/xiehengjian/multica",
	})
}
