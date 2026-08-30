package providerkit

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Releaser interface {
	Plan(ctx context.Context, plan StackPlan, report Reporter) (Plan, error)

	Provision(ctx context.Context, plan StackPlan, report Reporter) (StackResult, error)

	PlanDestroy(ctx context.Context, ref StackRef, report Reporter) (Plan, error)

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

	Edge edge.Edge

	Tags map[string]string

	Resources []Resource

	Uploads []Upload

	Images ImagePlan

	Links LinkReader

	App *AppPlan

	Options any
}

type LinkReader interface {
	Names(ctx context.Context) ([]string, error)

	Published(ctx context.Context) ([]Link, error)

	Resolve(ctx context.Context, link string) (Link, error)
}

type AppPlan struct {
	App        string
	Framework  string
	Entry      string
	Deployment string
	Compute    Compute
	Functions  []FunctionSpec

	Image           string
	HealthCheckPath string

	Values AppValues

	Grants []Link

	Routing  *RoutingPlan
	Guard    *OriginGuard
	ISR      *ISRPlan
	Bytecode *BytecodePlan

	AssetPrefix string

	Membrane ArtifactRef

	Packed any

	CrossesMembrane bool
}

type RoutingPlan struct {
	Entry    string
	Manifest []byte
}

type OriginGuard struct {
	Entry string
}

type ISRPlan struct {
	Prefix       string
	TagNamespace string
}

type BytecodePlan struct {
	Prefix string
}

type AppValues struct {
	Plain     map[string]string
	Sensitive map[string]string
	Secrets   []SecretRef
	Links     []Link
	Owners    map[string]string
	Folder    string
}

type SecretRef struct {
	Key    string
	Folder string
}

type FunctionSpec struct {
	Name     string
	Route    string
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

	Containers []AppContainer
}

type Link struct {
	Type       LinkType          `json:"type"`
	Name       string            `json:"name"`
	Resource   string            `json:"resource,omitempty"`
	Source     string            `json:"source,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	Grants     []Grant           `json:"grants,omitempty"`
	Version    int64             `json:"version,omitempty"`
}

type Grant struct {
	Label      string           `json:"label,omitempty"`
	Actions    []string         `json:"actions,omitempty"`
	Resources  []string         `json:"resources,omitempty"`
	Conditions []GrantCondition `json:"conditions,omitempty"`
}

type GrantCondition struct {
	Operator string   `json:"operator,omitempty"`
	Key      string   `json:"key,omitempty"`
	Values   []string `json:"values,omitempty"`
}

type Function struct {
	Name     string `json:"name"`
	Physical string `json:"physical,omitempty"`
	URL      string `json:"url,omitempty"`
}

type AppContainer struct {
	Name     string `json:"name"`
	Physical string `json:"physical,omitempty"`
	URL      string `json:"url,omitempty"`
	Image    string `json:"image,omitempty"`
}

type StackState struct {
	Present bool
	Result  StackResult
}
