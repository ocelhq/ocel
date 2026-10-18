package containers

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

const (
	fixtureOrigin   = "internal-ocel-containers-production-123.us-east-1.elb.amazonaws.com"
	fixtureFront    = "vo_2XyZ3abc4DEF5ghi"
	fixtureListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-containers-production/abc/def"
)

var fixtureRanges = []string{"198.51.100.0/24", "203.0.113.0/24"}

var mockARNs = map[string]string{
	"aws:ecs/cluster:Cluster":          "cluster/",
	"aws:lb/loadBalancer:LoadBalancer": "loadbalancer/app/",
	"aws:lb/listener:Listener":         "listener/app/",
	"aws:lb/targetGroup:TargetGroup":   "targetgroup/",
	"aws:cloudwatch/logGroup:LogGroup": "log-group:",
	"aws:acm/certificate:Certificate":  "certificate/",
}

func mockAccountARN(service, suffix string) string {
	return "arn:aws:" + service + ":us-east-1:" + componenttest.Account + ":" + suffix
}

type inputRecorder struct {
	mu       sync.Mutex
	recorded map[string]resource.PropertyMap
	attached []string
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
	if args.TypeToken == "aws:lambda/function:Function" {
		state := args.Inputs.Copy()
		state["arn"] = resource.NewStringProperty(mockAccountARN("lambda", "function:"+args.Name))
		return args.Name + "-id", state, nil
	}
	if arns, known := mockARNs[args.TypeToken]; known {
		state := args.Inputs.Copy()
		state["arn"] = resource.NewStringProperty(mockAccountARN(arns, args.Name))
		if args.TypeToken == "aws:lb/loadBalancer:LoadBalancer" {
			state["dnsName"] = resource.NewStringProperty(args.Name + ".us-east-1.elb.amazonaws.com")
		}
		if _, named := state["name"]; !named {
			name := args.Name
			if prefix, ok := state["namePrefix"]; ok && prefix.IsString() {
				name = prefix.StringValue() + args.Name
			}
			state["name"] = resource.NewStringProperty(name)
		}
		return args.Name + "-id", state, nil
	}
	if args.TypeToken == "aws:iam/role:Role" {
		state := args.Inputs.Copy()
		state["arn"] = resource.NewStringProperty("arn:aws:iam::" + componenttest.Account + ":role/" + args.Name)
		return args.Name + "-id", state, nil
	}
	if args.TypeToken == "aws:iam/rolePolicyAttachment:RolePolicyAttachment" {
		if arn, ok := args.Inputs["policyArn"]; ok && arn.IsString() {
			r.attached = append(r.attached, arn.StringValue())
		}
	}
	return args.Name + "-id", componenttest.Autonamed(args), nil
}

func (r *inputRecorder) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func recordedOf(t *testing.T, rec *inputRecorder, typeToken string) resource.PropertyMap {
	t.Helper()
	for key, inputs := range rec.recorded {
		if strings.HasPrefix(key, typeToken+"::") {
			return inputs
		}
	}
	t.Fatalf("the program declared no %s among %d resources", typeToken, len(rec.recorded))
	return nil
}

func fixtureInfrastructure() Infrastructure {
	return Infrastructure{
		VPC:           "vpc-123",
		Subnets:       []string{"subnet-a", "subnet-b"},
		Listener:      fixtureListener,
		OriginHost:    fixtureOrigin,
		VPCOrigin:     fixtureFront,
		TaskSecurity:  "sg-tasks",
		Cluster:       "arn:aws:ecs:us-east-1:123456789012:cluster/ocel-containers-production",
		ExecutionRole: "arn:aws:iam::123456789012:role/ocel-containers-production-exec-abc",
		LogGroup:      "/ocel/containers/production",
	}
}

func infrastructureOutputs() auto.OutputMap {
	infra := fixtureInfrastructure()
	subnets, _ := json.Marshal(infra.Subnets)
	return auto.OutputMap{
		outputKeyVPC:           auto.OutputValue{Value: infra.VPC},
		outputKeySubnets:       auto.OutputValue{Value: string(subnets)},
		outputKeyListener:      auto.OutputValue{Value: infra.Listener},
		outputKeyOriginHost:    auto.OutputValue{Value: infra.OriginHost},
		outputKeyVPCOrigin:     auto.OutputValue{Value: infra.VPCOrigin},
		outputKeyTaskSecurity:  auto.OutputValue{Value: infra.TaskSecurity},
		outputKeyCluster:       auto.OutputValue{Value: infra.Cluster},
		outputKeyExecutionRole: auto.OutputValue{Value: infra.ExecutionRole},
		outputKeyLogGroup:      auto.OutputValue{Value: infra.LogGroup},
	}
}

func assertDefaultSubnetsOnly(t *testing.T, filters [][]string) {
	t.Helper()
	if len(filters) != 1 {
		t.Fatalf("looked subnets up %d times, want once", len(filters))
	}
	if !slices.Contains(filters[0], "default-for-az") {
		t.Errorf("subnet lookup filtered on %v, want default-for-az too: a private subnet Ocel adds to the VPC must never enter a database subnet group, a cluster or a load balancer", filters[0])
	}
}
