package deploy

import (
	"fmt"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
)

const (
	ocelPackage = "ocel"
	ocelVersion = "1.0.0"
)

func newOcelPlugin(pending *site.PendingSets, statements postgres.StatementsAPI) (pulumi.Plugin, error) {
	built, err := infer.NewProviderBuilder().
		WithNamespace(ocelPackage).
		WithResources(site.AssetSetResource(pending), postgres.RoleResource(statements), component.RandomTagResource()).
		Build()
	if err != nil {
		return pulumi.Plugin{}, fmt.Errorf("build the in-process provider the engine pushes uploads, creates database roles and mints random tags through: %w", err)
	}
	return pulumi.Plugin{Package: ocelPackage, Version: ocelVersion, Provider: built}, nil
}
