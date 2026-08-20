package origin

import "context"

type Facts struct {
	Kind        Kind
	Defaults    map[Role]Kind
	DefaultEdge Kind
	Speaks      []Protocol
	Identity    string
}

type Origin interface {
	Facts() Facts
	Native() []Backing
	Bootstrap(ctx context.Context, class Class) (Substrate, error)
	Teardown(ctx context.Context, class Class) error
	Reconcile(ctx context.Context, spec DeploySpec, prior State) (Deployment, error)
	Open(state State) (Deployment, error)
	Records() RecordStore
}

type Substrate struct {
	Identity Identity
	Values   map[string]string
}

type DeploySpec struct {
	Slug     string
	Class    Class
	Manifest []byte
	Options  []byte
	Links    []Link
	Env      map[string]string
}

type State struct {
	Slug      string                   `json:"slug"`
	Class     Class                    `json:"class"`
	Identity  Identity                 `json:"identity,omitzero"`
	Resources map[string]ResourceState `json:"resources,omitempty"`
	Fronts    map[string]string        `json:"fronts,omitempty"`
	Adapter   Private                  `json:"adapter,omitzero"`
}

type Deployment interface {
	State() State
	Identity() Identity
	FunctionURL(route string) (string, error)
	Promote(ctx context.Context, promotion string) error
	Destroy(ctx context.Context) error
}

type RecordStore interface {
	Read(ctx context.Context, slug string, class Class) (Record, bool, error)
	Write(ctx context.Context, slug string, class Class, record Record) error
	Delete(ctx context.Context, slug string, class Class) error
}

type Record struct {
	Origin State   `json:"origin"`
	Edge   Private `json:"edge,omitzero"`
	Hosts  Private `json:"hosts,omitzero"`
}
