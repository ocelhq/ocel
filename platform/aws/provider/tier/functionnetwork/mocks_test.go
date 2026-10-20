package functionnetwork

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

type inputRecorder struct {
	mu       sync.Mutex
	recorded map[string]resource.PropertyMap
}

func (r *inputRecorder) registered(typeToken string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for key := range r.recorded {
		if name, found := strings.CutPrefix(key, typeToken+"::"); found {
			names = append(names, name)
		}
	}
	return names
}

func (r *inputRecorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recorded == nil {
		r.recorded = map[string]resource.PropertyMap{}
	}
	r.recorded[args.TypeToken+"::"+args.Name] = args.Inputs
	return args.Name + "-id", componenttest.Autonamed(args), nil
}

func (r *inputRecorder) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (r *inputRecorder) inputs(typeToken, name string) resource.PropertyMap {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recorded[typeToken+"::"+name]
}

func (r *inputRecorder) object(t *testing.T, typeToken, name, key string) resource.PropertyMap {
	t.Helper()
	value, ok := r.inputs(typeToken, name)[resource.PropertyKey(key)]
	if !ok || !value.IsObject() {
		t.Fatalf("%s on %s = %v, want an object", key, name, value)
	}
	return value.ObjectValue()
}
