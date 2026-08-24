package deploy

import (
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	pulumi "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const mockAccount = "123456789012"

func mockAccountARN(service, suffix string) string {
	return "arn:aws:" + service + ":us-east-1:" + mockAccount + ":" + suffix
}

type inputRecorder struct {
	mu       sync.Mutex
	recorded map[string]resource.PropertyMap
	attached []string
}

func (r *inputRecorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recorded == nil {
		r.recorded = map[string]resource.PropertyMap{}
	}
	r.recorded[args.TypeToken+"::"+args.Name] = args.Inputs
	if args.TypeToken == "aws:lambda/functionUrl:FunctionUrl" {
		state := args.Inputs.Copy()
		state["functionUrl"] = resource.NewStringProperty("https://" + args.Name + ".lambda-url.us-east-1.on.aws/")
		return args.Name + "-id", state, nil
	}
	if args.TypeToken == "aws:lambda/function:Function" {
		state := args.Inputs.Copy()
		state["arn"] = resource.NewStringProperty(mockAccountARN("lambda", "function:"+args.Name))
		return args.Name + "-id", state, nil
	}
	if args.TypeToken == "aws:lambda/layerVersion:LayerVersion" {
		state := args.Inputs.Copy()
		state["arn"] = resource.NewStringProperty(mockAccountARN("lambda", "layer:"+args.Name+":1"))
		return args.Name + "-id", state, nil
	}
	if args.TypeToken == "aws:iam/role:Role" {
		state := args.Inputs.Copy()
		state["arn"] = resource.NewStringProperty("arn:aws:iam::" + mockAccount + ":role/" + args.Name)
		return args.Name + "-id", state, nil
	}
	if args.TypeToken == "aws:iam/rolePolicyAttachment:RolePolicyAttachment" {
		if arn, ok := args.Inputs["policyArn"]; ok && arn.IsString() {
			r.attached = append(r.attached, arn.StringValue())
		}
	}
	return args.Name + "-id", args.Inputs, nil
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

func stringsAt(m resource.PropertyMap, key string) []string {
	value, ok := m[resource.PropertyKey(key)]
	if !ok || !value.IsArray() {
		return nil
	}
	out := make([]string, 0, len(value.ArrayValue()))
	for _, item := range value.ArrayValue() {
		if item.IsString() {
			out = append(out, item.StringValue())
		}
	}
	return out
}
