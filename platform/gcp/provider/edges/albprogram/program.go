package albprogram

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
)

type Program struct {
	Run     func(ctx *pulumi.Context, project string) error
	Patches *ocelpulumi.Patches
}
