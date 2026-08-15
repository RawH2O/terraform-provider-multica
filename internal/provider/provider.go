package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

var _ provider.Provider = (*multicaProvider)(nil)

func New() provider.Provider {
	return &multicaProvider{}
}

type multicaProvider struct {
	version string
}

type providerModel struct {
	ServerURL   types.String `tfsdk:"server_url"`
	Token       types.String `tfsdk:"token"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
}

func (p *multicaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "multica"
	resp.Version = p.version
}

func (p *multicaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"server_url": schema.StringAttribute{
				Optional:    true,
				Description: "Multica API base URL. Can also be set with MULTICA_SERVER_URL.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Multica API token. Can also be set with MULTICA_API_TOKEN.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:    true,
				Description: "Workspace UUID. Can also be set with MULTICA_WORKSPACE_ID.",
			},
		},
	}
}

func (p *multicaProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	serverURL := valueOrEnv(data.ServerURL, "MULTICA_SERVER_URL")
	token := valueOrEnv(data.Token, "MULTICA_API_TOKEN")
	workspaceID := valueOrEnv(data.WorkspaceID, "MULTICA_WORKSPACE_ID")
	if serverURL == "" {
		resp.Diagnostics.AddError("Missing Multica server URL", "Set server_url or MULTICA_SERVER_URL.")
		return
	}
	if token == "" {
		resp.Diagnostics.AddError("Missing Multica API token", "Set token or MULTICA_API_TOKEN.")
		return
	}
	if workspaceID == "" {
		resp.Diagnostics.AddError("Missing Multica workspace ID", "Set workspace_id or MULTICA_WORKSPACE_ID.")
		return
	}

	resp.DataSourceData = client.New(serverURL, token, workspaceID, nil)
	resp.ResourceData = client.New(serverURL, token, workspaceID, nil)
}

func (p *multicaProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newAgentResource,
	}
}

func (p *multicaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

func valueOrEnv(value types.String, name string) string {
	if !value.IsNull() && !value.IsUnknown() && value.ValueString() != "" {
		return value.ValueString()
	}
	return os.Getenv(name)
}
