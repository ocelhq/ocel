package providerkit

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
)

// Releaser provisions the stacks of a release, one stack per call, and destroys
// them again. It is the only port that mutates app infrastructure, and the only
// one the Pulumi adapter implements — which is why Pulumi is opt-in composition
// and not a requirement.
//
// The unit is a stack, not a release, because that is the unit every engine
// already diffs whole and the unit the kit already sequences: the infra stack
// comes up first, its links become the grants of every app stack, and the app
// stacks fan out. Handing the port the whole release would mean every vendor
// re-implementing that choreography; handing it one resource would hand it the
// engine's diff. A stack is exactly what neither side wants to own for the other.
//
// A port never sees a proto message. The plan arrives validated, with every
// name already chosen through pkg/naming and the class already resolved.
type Releaser interface {
	// Provision brings one stack up. It converges: the same plan applied twice
	// is the second one doing nothing. Whatever it left behind on failure is
	// the engine's to reconcile on the next call — the kit does not retry.
	Provision(ctx context.Context, plan StackPlan, report Reporter) (StackResult, error)

	// Destroy tears one stack down and forgets it. A stack that is not there is
	// not an error: the port says so and returns nil, because the kit's records
	// and the engine's state are allowed to disagree after a crash and a rerun
	// has to get past that.
	Destroy(ctx context.Context, ref StackRef, report Reporter) error
}

// StackRef names one stack without describing it. Name carries env, app and
// release; Project and Class are what an engine needs to find where the stack's
// state is kept.
type StackRef struct {
	Project string
	Class   Class
	Name    naming.StackName
}

// StackKind says what a plan's payload is. An infra stack carries the resources
// an env shares across releases; an app stack carries one app's compute for one
// release. Which is which is already in the name, but the payload is typed by
// kind so a program reads the one it was handed and not a union.
type StackKind string

const (
	StackInfra StackKind = "infra"
	StackApp   StackKind = "app"
)

// StackPlan is one stack of a release, resolved.
type StackPlan struct {
	Ref  StackRef
	Kind StackKind

	// Tags are what every resource the stack makes should carry, rendered by the
	// kit from naming.Coordinate and naming.Facts. How they are applied is the
	// vendor's — a default-tags setting, a label map, nothing on a bare host.
	Tags map[string]string

	// Resources is the infra payload: the manifest's own resources, already
	// filtered to the ones this env owns. A linked resource is not here; it is
	// consumed, and shows up in an app plan's Grants instead.
	Resources []Resource

	// App is the app payload, present when Kind is StackApp.
	App *AppPlan
}

// Resource is one thing the manifest asked for. Type says which spec is set; the
// specs are the kit's vocabulary for each primitive the wire enumerates, and
// providerkit/resources hands them to per-primitive functions unchanged.
type Resource struct {
	Name string
	Type LinkType

	Postgres  *PostgresSpec
	Bucket    *BucketSpec
	Container *ContainerSpec
	Custom    *CustomSpec
}

type PostgresSpec struct {
	Version string
}

type BucketSpec struct{}

type ContainerSpec struct {
	Image string
	Port  int
	Env   map[string]string
}

// CustomSpec is the wire's escape hatch carried through: a type the enum has no
// word for, with its properties opaque.
type CustomSpec struct {
	Type   string
	Config map[string]any
}

// AppPlan is one app's compute for one release.
//
// TODO(#516): Serving is the per-app facts the AWS program reads today (routing
// manifest, ISR prefix and write secret, bytecode cache, asset prefixes, the
// membrane placement). They are kit facts — the deployment record carries them —
// but their exact set lands when the app program migrates, not here.
type AppPlan struct {
	App       string
	Framework string
	Entry     string
	Functions []FunctionSpec

	// Grants are the links this app's compute may reach: the infra stack's
	// results plus anything consumed from another env. The vendor turns them
	// into whatever its compute needs — IAM policies, a network rule, an env
	// var — and never decides which links an app gets.
	Grants []Link
}

type FunctionSpec struct {
	Name     string
	Handler  string
	Runtime  string
	Artifact ArtifactRef
	Env      map[string]string
	Memory   int
	Timeout  time.Duration

	// URL asks for a public invoke URL. Not every function is an entry point.
	URL bool
}

// StackResult is what the kit needs back from a stack to record the release,
// publish its links and promote it.
type StackResult struct {
	// Links is one per Resource in an infra plan, in no particular order.
	Links []Link

	// Functions is one per FunctionSpec in an app plan.
	Functions []Function
}

// Link is a made resource as the membrane will serve it. Properties are keyed by
// the wire's own field names for the type — host, port, database, username,
// password for postgres; the kit checks the required keys when it publishes the
// link and refuses a result missing one by name. The values are opaque to the
// kit, which is what lets a vendor resolve a password from its own secret store
// inside its decoder and the kit never know.
type Link struct {
	Type       LinkType
	Name       string
	Properties map[string]string
}

// Function is one provisioned function. Physical is the vendor's own name for
// it, which the kit stores and hands back to Warmer and CodeEmbedder; URL is set
// when the spec asked for one.
type Function struct {
	Name     string
	Physical string
	URL      string
}

// StackState is StackInspector's answer. Present false means the engine has never seen
// this stack, or has forgotten it; Result is then zero.
type StackState struct {
	Present bool
	Result  StackResult
}

// RequiredProperties is the kit's one rule about link shape: the keys a Link of
// each type must carry before the kit will publish it.
func RequiredProperties(t LinkType) []string {
	switch t {
	case "postgres":
		return []string{"host", "port", "database", "username", "password"}
	case "bucket":
		return []string{"name"}
	}
	return nil
}
