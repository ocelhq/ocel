package component

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Resource interface {
	Plan(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error)
	Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error)
	Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error
}

func Hooks(resource Resource) *resources.ResourceHooks {
	return &resources.ResourceHooks{Plan: resource.Plan, Provision: resource.Provision, Remove: resource.Remove}
}

type Box struct {
	SSH        *ssh.Box
	Containers *container.Containers
	Apps       *release.Containers
	Routing    *routing.Table
	KeyValues  records.Store
	Cipher     *boxstore.Cipher
	Transform  transform.Pass
}
