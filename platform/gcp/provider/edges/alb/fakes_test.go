package alb

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const frontAddress = "34.117.0.7"

type world struct {
	mu       sync.Mutex
	outputs  map[string]map[string]string
	ups      []string
	destroys []string
	routed   map[string]map[string]string
	pinned   []string
	refuseUp error
}

func newWorld() *world {
	return &world{
		outputs: map[string]map[string]string{
			FrontStack(edge.ClassProduction): front(),
			FrontStack(edge.ClassPreview):    front(),
		},
		routed: map[string]map[string]string{},
	}
}

func front() map[string]string {
	return map[string]string{
		"address":        frontAddress,
		"certificateMap": "ocel-alb-production-certs",
		"urlMap":         "ocel-alb-production-routes",
		"notFound":       "ocel-alb-production-notfound",
	}
}

func (w *world) Up(_ context.Context, _ edge.Class, stack string, program Program, _ edge.Reporter) (map[string]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refuseUp != nil {
		return nil, w.refuseUp
	}
	if program == nil {
		return nil, errors.New("a stack was raised with no program to raise")
	}
	w.ups = append(w.ups, stack)
	if held, standing := w.outputs[stack]; standing {
		return maps.Clone(held), nil
	}
	w.outputs[stack] = map[string]string{}
	return map[string]string{}, nil
}

func (w *world) Destroy(_ context.Context, _ edge.Class, stack string, _ edge.Reporter) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.destroys = append(w.destroys, stack)
	return nil
}

func (w *world) Outputs(_ context.Context, _ edge.Class, stack string) (map[string]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.outputs[stack]), nil
}

func (w *world) Route(_ context.Context, urlMap, hostname, backend string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	hosts := w.routed[urlMap]
	if hosts == nil {
		hosts = map[string]string{}
		w.routed[urlMap] = hosts
	}
	hosts[hostname] = backend
	return nil
}

func (w *world) Unroute(_ context.Context, urlMap, hostname string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.routed[urlMap], hostname)
	return nil
}

func (w *world) Pin(_ context.Context, service, revision string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pinned = append(w.pinned, service+"@"+revision)
	return nil
}

func (w *world) raised() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.ups)
}

func (w *world) torn() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.destroys)
}

func (w *world) hosts(urlMap string) map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.routed[urlMap])
}

func (w *world) pins() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.pinned)
}

var _ Stacks = (*world)(nil)

var _ Routes = (*world)(nil)

func declared(program Program) (map[string]declaration, error) {
	seen := map[string]declaration{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return program(ctx)
	}, pulumi.WithMocks("alb", "test", mocks{mu: &sync.Mutex{}, seen: seen}))
	return seen, err
}

type declaration struct {
	Token string
	Args  map[string]any
}

type mocks struct {
	mu   *sync.Mutex
	seen map[string]declaration
}

func (m mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[args.Name] = declaration{Token: args.TypeToken, Args: args.Inputs.Mappable()}
	outputs := args.Inputs.Copy()
	outputs["selfLink"] = resource.NewStringProperty("https://compute.example.com/" + args.Name)
	if args.TypeToken == "gcp:compute/globalAddress:GlobalAddress" {
		outputs["address"] = resource.NewStringProperty(frontAddress)
	}
	return args.Name + "-id", outputs, nil
}

func (mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}
