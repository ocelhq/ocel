package componenttest

import (
	"regexp"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const (
	BoundaryARN    = "arn:aws:iam::123456789012:policy/ocel-app-boundary"
	AppRolePath    = "/ocel/production/app/"
	Account        = "123456789012"
	AutonameSuffix = "0a1b2c3d"
	RandomTag      = "k3x9q2"
	StateTableARN  = "arn:aws:dynamodb:us-east-1:1234:table/ocel-state"
)

var lambdaStatementID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func LambdaAcceptsStatementID(permission resource.PropertyMap) (string, bool) {
	id, ok := permission["statementId"]
	if !ok || !id.IsString() {
		return "", false
	}
	return id.StringValue(), lambdaStatementID.MatchString(id.StringValue())
}

func Autonamed(args pulumi.MockResourceArgs) resource.PropertyMap {
	state := args.Inputs.Copy()
	if args.TypeToken == component.RandomTagToken {
		state["tag"] = resource.NewStringProperty(RandomTag)
		return state
	}
	if _, named := state["name"]; named {
		return state
	}
	if prefix, ok := state["namePrefix"]; ok && prefix.IsString() {
		state["name"] = resource.NewStringProperty(prefix.StringValue() + AutonameSuffix)
	}
	return state
}

type Recorder struct {
	mu        sync.Mutex
	tags      map[string]map[string]string
	inputs    map[string]resource.PropertyMap
	registers map[string]pulumi.MockResourceArgs
	tokens    []string
	outputs   func(pulumi.MockResourceArgs) resource.PropertyMap
}

func (r *Recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tags == nil {
		r.tags = map[string]map[string]string{}
		r.inputs = map[string]resource.PropertyMap{}
		r.registers = map[string]pulumi.MockResourceArgs{}
	}
	r.inputs[args.TypeToken+"::"+args.Name] = args.Inputs
	r.registers[args.TypeToken+"::"+args.Name] = args
	if !slices.Contains(r.tokens, args.TypeToken) {
		r.tokens = append(r.tokens, args.TypeToken)
	}
	recorded := map[string]string{}
	if raw, ok := args.Inputs["tags"]; ok && raw.IsObject() {
		for key, value := range raw.ObjectValue() {
			if value.IsString() {
				recorded[string(key)] = value.StringValue()
			}
		}
	}
	r.tags[args.TypeToken+"::"+args.Name] = recorded
	state := Autonamed(args)
	if r.outputs != nil {
		for key, value := range r.outputs(args) {
			state[key] = value
		}
	}
	return args.Name + "-id", state, nil
}

func (r *Recorder) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (r *Recorder) Component(t *testing.T, typeToken, name string) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	tags, ok := r.tags[typeToken+"::"+name]
	if !ok {
		t.Fatalf("no %s named %q was registered", typeToken, name)
	}
	return tags[component.TagComponent]
}

func (r *Recorder) Tags(t *testing.T, typeToken, name string) map[string]string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	tags, ok := r.tags[typeToken+"::"+name]
	if !ok {
		t.Fatalf("no %s named %q was registered", typeToken, name)
	}
	return tags
}

func (r *Recorder) InputsOf(t *testing.T, typeToken, name string) resource.PropertyMap {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	inputs, ok := r.inputs[typeToken+"::"+name]
	if !ok {
		t.Fatalf("no %s named %q was registered", typeToken, name)
	}
	return inputs
}

func (r *Recorder) IsRegistered(typeToken, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.inputs[typeToken+"::"+name]
	return ok
}

func (r *Recorder) RetainsOnDelete(typeToken, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	registered := r.registers[typeToken+"::"+name].RegisterRPC
	return registered != nil && registered.GetRetainOnDelete()
}

func (r *Recorder) DependenciesOf(typeToken, name string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	registered := r.registers[typeToken+"::"+name].RegisterRPC
	if registered == nil {
		return nil
	}
	return registered.GetDependencies()
}

func (r *Recorder) ProviderOf(typeToken, name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registers[typeToken+"::"+name].Provider
}

func (r *Recorder) Tokens() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(slices.Values(r.tokens))
}

func NewRecorder(outputs func(pulumi.MockResourceArgs) resource.PropertyMap) *Recorder {
	return &Recorder{outputs: outputs}
}

func Record(t *testing.T, program pulumi.RunFunc, outputs ...func(pulumi.MockResourceArgs) resource.PropertyMap) *Recorder {
	t.Helper()
	rec := &Recorder{}
	if len(outputs) == 1 {
		rec.outputs = outputs[0]
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--infra", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}
	return rec
}
