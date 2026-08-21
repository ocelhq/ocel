package providerkit

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
)

type Releaser interface {
	Provision(ctx context.Context, plan StackPlan, report Reporter) (StackResult, error)

	Destroy(ctx context.Context, ref StackRef, report Reporter) error
}

type StackRef struct {
	Project string
	Class   Class
	Name    naming.StackName
}

type StackKind string

const (
	StackInfra StackKind = "infra"
	StackApp   StackKind = "app"
)

type StackPlan struct {
	Ref  StackRef
	Kind StackKind

	Tags map[string]string

	Resources []Resource

	Links LinkReader

	App *AppPlan
}

type LinkReader interface {
	Published(ctx context.Context) ([]Link, error)

	Resolve(ctx context.Context, link, property string) (string, error)
}

type AppPlan struct {
	App       string
	Framework string
	Entry     string
	Functions []FunctionSpec

	Values AppValues

	Grants []Link
}

type AppValues struct {
	Plain     map[string]string
	Sensitive map[string]string
	Secrets   map[string]string
	Links     []Link
	Owners    map[string]string
}

type FunctionSpec struct {
	Name     string
	Handler  string
	Runtime  string
	Artifact ArtifactRef
	Env      map[string]string
	Memory   int
	Timeout  time.Duration

	URL bool
}

type StackResult struct {
	Links []Link

	Functions []Function
}

type Link struct {
	Type       LinkType
	Name       string
	Properties map[string]string
}

type Function struct {
	Name     string
	Physical string
	URL      string
}

type StackState struct {
	Present bool
	Result  StackResult
}
