package provider

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Stacks interface {
	Plan(ctx context.Context, spec StackSpec, progress edge.Progress) (Plan, error)

	Provision(ctx context.Context, spec StackSpec, progress edge.Progress) (StackResult, error)

	PlanDestroy(ctx context.Context, ref StackRef, progress edge.Progress) (Plan, error)

	Destroy(ctx context.Context, ref StackRef, progress edge.Progress) error
}

type StackRef struct {
	Project string
	Class   edge.Class
	Name    naming.StackName
}

type StackKind string

const (
	StackInfra StackKind = "infra"
	StackApp   StackKind = "app"
)

type StackSpec struct {
	Ref  StackRef
	Kind StackKind

	Edge edge.Edge

	Tags map[string]string

	Resources []Resource

	Uploads []Upload

	Images ImagePushes

	Bindings Bindings

	App *AppSpec

	VendorState any
}

type Bindings interface {
	Names(ctx context.Context) ([]string, error)

	Published(ctx context.Context) ([]Binding, error)

	Named(ctx context.Context, binding string) (Binding, error)
}

type AppSpec struct {
	App        string
	Framework  string
	Entry      string
	Deployment string
	Compute    Compute
	Functions  []FunctionSpec

	Image           string
	HealthCheckPath string
	Arch            string

	Values AppValues

	Grants []Binding

	Routing  *RoutingSpec
	Guard    *OriginGuard
	ISR      *ISRSpec
	Bytecode *BytecodeSpec

	AssetPrefix string

	PreviewLabel string

	VendorState any

	Proxied bool
}

type RoutingSpec struct {
	Entry    string
	Manifest []byte
}

type OriginGuard struct {
	Entry string
}

type ISRSpec struct {
	Prefix       string
	TagNamespace string
}

type BytecodeSpec struct {
	Prefix string
}

type AppValues struct {
	Plain     map[string]string
	Sensitive map[string]string
	Secrets   []SecretRef
	Bindings  []Binding
	Owners    map[string]string
	Folder    string
	Delivered map[string]string
	Phase     string
}

func (v AppValues) Injected() map[string]string {
	if v.Phase == "" {
		return nil
	}
	return map[string]string{constants.PhaseEnvName: v.Phase}
}

func (v AppValues) String() string {
	return fmt.Sprintf("values folder %q plain %v sensitive %v secrets %v bindings %v delivered %d entries [redacted]",
		v.Folder, slices.Sorted(maps.Keys(v.Plain)), slices.Sorted(maps.Keys(v.Sensitive)),
		secretNames(v.Secrets), bindingNames(v.Bindings), len(v.Delivered))
}

func (v AppValues) GoString() string { return v.String() }

func secretNames(refs []SecretRef) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Key)
	}
	return names
}

func bindingNames(bindings []Binding) []string {
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, binding.Name)
	}
	return names
}

type SecretRef struct {
	Key    string
	Folder string
}

type FunctionSpec struct {
	Name      string
	Route     string
	Handler   string
	Framework appbuild.Framework
	Artifact  ArtifactRef
	Image     string
	Env       map[string]string
	Memory    int
	Timeout   time.Duration
}

type StackResult struct {
	Bindings []Binding

	Functions []Function

	Containers []AppContainer

	EdgeBundleKey string

	Envelope string

	ISRWriteSecret string
}

type Binding struct {
	Type       BindingType       `json:"type"`
	Name       string            `json:"name"`
	Resource   string            `json:"resource,omitempty"`
	Source     string            `json:"source,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	Grants     []Grant           `json:"grants,omitempty"`
	Version    int64             `json:"version,omitempty"`
	Wire       []byte            `json:"-"`
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
	Revision string `json:"revision,omitempty"`
}

type AppContainer struct {
	Name     string `json:"name"`
	Physical string `json:"physical,omitempty"`
	URL      string `json:"url,omitempty"`
	Image    string `json:"image,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type StackState struct {
	Present bool
	Result  StackResult
}
