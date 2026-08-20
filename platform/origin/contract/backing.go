package origin

import "context"

type BackingFacts struct {
	Role     Role
	Kind     Kind
	Native   Kind
	Protocol Protocol
	Brings   []string
}

func (f BackingFacts) Independent() bool { return f.Native == "" }

type Backing interface {
	Facts() BackingFacts
	Reconcile(ctx context.Context, spec ResourceSpec, prior ResourceState) (Resource, error)
	Open(state ResourceState) (Resource, error)
}

type ResourceSpec struct {
	Slug    string
	Class   Class
	Name    string
	Options []byte
	Config  []byte
}

type ResourceState struct {
	Name     string            `json:"name"`
	Kind     Kind              `json:"kind"`
	Endpoint string            `json:"endpoint,omitempty"`
	Values   map[string]string `json:"values,omitempty"`
	Adapter  Private           `json:"adapter,omitzero"`
}

type Resource interface {
	State() ResourceState
	Link(ctx context.Context, to Identity) (Link, error)
	Destroy(ctx context.Context) error
}

type Link struct {
	Role     Role
	Name     string
	Protocol Protocol
	Endpoint string
	Values   map[string]string
	Secrets  map[string]string
	Granted  bool
}

type Identity struct {
	Origin    Kind
	Principal string
}
