package deploy

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	"github.com/ocelhq/ocel/platform/aws/provider/transform/transformtest"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"google.golang.org/protobuf/types/known/structpb"
)

func outputManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug:      "shop",
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "fn--api--users", App: "api"}},
	}
}

func networkStore(properties *linksv1.PostgresProperties) *recordingPublisher {
	if properties == nil {
		properties = &linksv1.PostgresProperties{}
	}
	return storeHolding(&linksv1.Link{
		Name: "network", Source: "sst", Properties: &linksv1.Link_Postgres{Postgres: properties},
	})
}

func customNetworkStore(t *testing.T, properties map[string]any) *recordingPublisher {
	t.Helper()
	custom, err := structpb.NewStruct(properties)
	if err != nil {
		t.Fatalf("build the published struct: %v", err)
	}
	return storeHolding(&linksv1.Link{
		Name: "network", Source: "sst", Properties: &linksv1.Link_Custom{Custom: custom},
	})
}

func storeHolding(link *linksv1.Link) *recordingPublisher {
	return &recordingPublisher{
		published: map[string][]string{"production": {"network"}},
		resolved:  map[string]vars.PublishedRecord{"network": {Link: link, Version: 3}},
	}
}

func outputConfig(store LinkStore, evaluator transform.Evaluator) Config {
	return Config{Env: "prod", VarsClass: "production", Links: store, Transform: evaluator}
}

func vpcPlaceholder(link, property string) map[string]any {
	return map[string]any{outputPlaceholderKey: map[string]any{"link": link, "property": property}}
}

func placedFunction(subnets, groups any) []transform.Surfaces {
	return []transform.Surfaces{{
		"lambda": {"memorySizeMb": float64(1024), "timeoutSeconds": float64(30), "runtime": "nodejs24.x"},
		"url":    {"invokeMode": "RESPONSE_STREAM"},
		"vpc":    {"subnetIds": subnets, "securityGroupIds": groups},
	}}
}

func TestResolveOutputs(t *testing.T) {
	t.Parallel()

	t.Run("a placeholder is filled from the property the published record carries", func(t *testing.T) {
		t.Parallel()

		manifest := outputManifest()
		store := customNetworkStore(t, map[string]any{
			"subnetIds":        []any{"subnet-a", "subnet-b"},
			"securityGroupIds": []any{"sg-1"},
		})
		fake := &fakeEvaluator{out: placedFunction(
			vpcPlaceholder("network", "subnetIds"),
			vpcPlaceholder("network", "securityGroupIds"),
		)}

		resolved, err := resolveTransforms(t.Context(), outputConfig(store, fake), manifest)
		if err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}

		got := resolved.forFunction(manifest.GetFunctions()[0]).VPC
		want := functionVPC{SubnetIDs: []string{"subnet-a", "subnet-b"}, SecurityGroupIDs: []string{"sg-1"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("vpc = %+v, want %+v", got, want)
		}
	})

	t.Run("a scalar property lands as a scalar beside a list that lands as a list", func(t *testing.T) {
		t.Parallel()

		manifest := outputManifest()
		store := customNetworkStore(t, map[string]any{
			"subnetIds":      []any{"subnet-a"},
			"primaryGroupId": "sg-1",
			"functionMemory": float64(2048),
		})
		surfaces := placedFunction(
			vpcPlaceholder("network", "subnetIds"),
			[]any{vpcPlaceholder("network", "primaryGroupId")},
		)
		surfaces[0]["lambda"]["memorySizeMb"] = vpcPlaceholder("network", "functionMemory")
		fake := &fakeEvaluator{out: surfaces}

		resolved, err := resolveTransforms(t.Context(), outputConfig(store, fake), manifest)
		if err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}

		args := resolved.forFunction(manifest.GetFunctions()[0])
		want := functionVPC{SubnetIDs: []string{"subnet-a"}, SecurityGroupIDs: []string{"sg-1"}}
		if !reflect.DeepEqual(args.VPC, want) {
			t.Errorf("vpc = %+v, want %+v", args.VPC, want)
		}
		if args.MemorySizeMB != 2048 {
			t.Errorf("memorySizeMb = %d, want the published number itself", args.MemorySizeMB)
		}
	})

	t.Run("a property the surface cannot decode fails the deploy by surface, field, link and property", func(t *testing.T) {
		t.Parallel()

		store := customNetworkStore(t, map[string]any{"subnetIds": float64(7)})
		fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})}

		_, err := resolveTransforms(t.Context(), outputConfig(store, fake), outputManifest())
		var shape *OutputShapeError
		if !errors.As(err, &shape) {
			t.Fatalf("resolveTransforms error = %v, want an OutputShapeError", err)
		}
		for _, fact := range []string{"fn--api--users", "vpc.subnetIds", "network", "subnetIds", "list of strings"} {
			if !strings.Contains(err.Error(), fact) {
				t.Errorf("error = %q, missing %q", err, fact)
			}
		}
	})

	t.Run("the store is read at the class and environment this deploy targets", func(t *testing.T) {
		t.Parallel()

		store := customNetworkStore(t, map[string]any{"subnetIds": []any{"subnet-a"}})
		fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})}
		cfg := outputConfig(store, fake)
		cfg.Class = deploymentsv1.Environment_CLASS_PREVIEW
		cfg.Identity = "pr-4"
		store.published = map[string][]string{"production": {"network"}}

		if _, err := resolveTransforms(t.Context(), cfg, outputManifest()); err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}
		if store.readEnvironment != "pr-4" {
			t.Errorf("records were read from %q, want the preview's own environment", store.readEnvironment)
		}
		if store.namesEnvironment != "pr-4" {
			t.Errorf("published names were read from %q, want the preview's own environment", store.namesEnvironment)
		}
	})

	t.Run("a deploy naming no output never reaches the store", func(t *testing.T) {
		t.Parallel()

		store := customNetworkStore(t, map[string]any{"subnetIds": []any{"subnet-a"}})
		fake := &fakeEvaluator{}
		if _, err := resolveTransforms(t.Context(), outputConfig(store, fake), outputManifest()); err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}
		if store.reads != 0 {
			t.Errorf("the store was read %d times, want it left alone", store.reads)
		}
	})

	t.Run("an output naming an unpublished link fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		store := customNetworkStore(t, map[string]any{"subnetIds": []any{"subnet-a"}})
		store.published = map[string][]string{"production": {"uploads"}}
		fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})}

		_, err := resolveTransforms(t.Context(), outputConfig(store, fake), outputManifest())
		var unpublished *UnpublishedOutputError
		if !errors.As(err, &unpublished) {
			t.Fatalf("resolveTransforms error = %v, want an UnpublishedOutputError", err)
		}
		for _, fact := range []string{"fn--api--users", "vpc.subnetIds", "network", "subnetIds", "production", "uploads"} {
			if !strings.Contains(err.Error(), fact) {
				t.Errorf("error = %q, missing %q", err, fact)
			}
		}
	})

	t.Run("a link published to another class is named as such", func(t *testing.T) {
		t.Parallel()

		store := customNetworkStore(t, map[string]any{"subnetIds": []any{"subnet-a"}})
		store.published = map[string][]string{"preview": {"network"}}
		fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})}
		cfg := outputConfig(store, fake)
		cfg.VarsSiblingClasses = []string{"production", "preview"}

		_, err := resolveTransforms(t.Context(), cfg, outputManifest())
		var unpublished *UnpublishedOutputError
		if !errors.As(err, &unpublished) {
			t.Fatalf("resolveTransforms error = %v, want an UnpublishedOutputError", err)
		}
		if !reflect.DeepEqual(unpublished.FoundIn, []string{"preview"}) {
			t.Errorf("cross-class probe = %v, want the class the record was published to instead", unpublished.FoundIn)
		}
		if !strings.Contains(err.Error(), "published to preview instead") {
			t.Errorf("error = %q, want the probe's finding in the message", err)
		}
	})

	t.Run("an output resolving to nothing fails the deploy rather than un-placing the function", func(t *testing.T) {
		t.Parallel()

		for name, published := range map[string]any{
			"blank string": " ",
			"empty list":   []any{},
			"empty map":    map[string]any{},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				store := customNetworkStore(t, map[string]any{"subnetIds": published})
				fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})}

				_, err := resolveTransforms(t.Context(), outputConfig(store, fake), outputManifest())
				var empty *EmptyOutputError
				if !errors.As(err, &empty) {
					t.Fatalf("resolveTransforms error = %v, want an EmptyOutputError", err)
				}
				for _, fact := range []string{"fn--api--users", "vpc.subnetIds", "network", "subnetIds"} {
					if !strings.Contains(err.Error(), fact) {
						t.Errorf("error = %q, missing %q", err, fact)
					}
				}
			})
		}
	})

	t.Run("an output naming a resource this deploy provisions fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		manifest := outputManifest()
		manifest.Resources = []*deploymentsv1.ManifestResource{{
			LogicalName: "network",
			Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "network"},
			Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
		}}
		store := customNetworkStore(t, map[string]any{"subnetIds": []any{"subnet-a"}})
		fake := &fakeEvaluator{out: []transform.Surfaces{
			{
				"bucket":       {"forceDestroy": false},
				"cors":         {"allowedOrigins": []any{}, "allowedMethods": []any{}, "allowedHeaders": []any{}, "exposeHeaders": []any{}, "maxAgeSeconds": float64(0)},
				"listener":     {"timeoutSeconds": float64(30)},
				"notification": {"events": []any{}},
			},
			placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})[0],
		}}

		_, err := resolveTransforms(t.Context(), outputConfig(store, fake), manifest)
		var provisioned *ProvisionedOutputError
		if !errors.As(err, &provisioned) {
			t.Fatalf("resolveTransforms error = %v, want a ProvisionedOutputError", err)
		}
		if store.reads != 0 {
			t.Errorf("the store was read %d times, want the refusal before any read", store.reads)
		}
		for _, fact := range []string{"fn--api--users", "vpc.subnetIds", "network", "`links`"} {
			if !strings.Contains(err.Error(), fact) {
				t.Errorf("error = %q, missing %q", err, fact)
			}
		}
	})

	t.Run("an output naming a resource this deploy binds is read from the store", func(t *testing.T) {
		t.Parallel()

		manifest := outputManifest()
		manifest.Resources = []*deploymentsv1.ManifestResource{{
			LogicalName: "network",
			Linked:      true,
			Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "network"},
			Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
		}}
		store := customNetworkStore(t, map[string]any{"subnetIds": []any{"subnet-a"}})
		fake := &fakeEvaluator{out: []transform.Surfaces{
			{
				"bucket":       {"forceDestroy": false},
				"cors":         {"allowedOrigins": []any{}, "allowedMethods": []any{}, "allowedHeaders": []any{}, "exposeHeaders": []any{}, "maxAgeSeconds": float64(0)},
				"listener":     {"timeoutSeconds": float64(30)},
				"notification": {"events": []any{}},
			},
			placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})[0],
		}}

		resolved, err := resolveTransforms(t.Context(), outputConfig(store, fake), manifest)
		if err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}
		if got := resolved.forFunction(manifest.GetFunctions()[0]).VPC.SubnetIDs; !reflect.DeepEqual(got, []string{"subnet-a"}) {
			t.Errorf("subnetIds = %v, want the bound link's own record read", got)
		}
	})

	t.Run("an output naming a property the record does not carry fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		store := customNetworkStore(t, map[string]any{
			"subnetIds":        []any{"subnet-a"},
			"securityGroupIds": []any{"sg-1"},
		})
		fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "privateSubnetIds"), []any{"sg-1"})}

		_, err := resolveTransforms(t.Context(), outputConfig(store, fake), outputManifest())
		var missing *OutputPropertyError
		if !errors.As(err, &missing) {
			t.Fatalf("resolveTransforms error = %v, want an OutputPropertyError", err)
		}
		if want := []string{"securityGroupIds", "subnetIds"}; !reflect.DeepEqual(missing.Carries, want) {
			t.Errorf("carries = %v, want the published record's own keys %v", missing.Carries, want)
		}
		for _, fact := range []string{"fn--api--users", "vpc.subnetIds", "network", "privateSubnetIds", "securityGroupIds, subnetIds"} {
			if !strings.Contains(err.Error(), fact) {
				t.Errorf("error = %q, missing %q", err, fact)
			}
		}
	})

	t.Run("an output naming a property no owned type declares fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		store := networkStore(&linksv1.PostgresProperties{Host: "db.internal"})
		fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "privateSubnetIds"), []any{"sg-1"})}

		_, err := resolveTransforms(t.Context(), outputConfig(store, fake), outputManifest())
		var missing *OutputPropertyError
		if !errors.As(err, &missing) {
			t.Fatalf("resolveTransforms error = %v, want an OutputPropertyError", err)
		}
		if want := []string{"database", "host", "password", "port", "username"}; !reflect.DeepEqual(missing.Carries, want) {
			t.Errorf("carries = %v, want the typed record's own fields %v", missing.Carries, want)
		}
	})

	t.Run("a malformed placeholder fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		for name, authored := range map[string]any{
			"naming no link":     map[string]any{outputPlaceholderKey: map[string]any{"property": "privateSubnetIds"}},
			"naming no property": map[string]any{outputPlaceholderKey: map[string]any{"link": "network"}},
			"carrying no ref":    map[string]any{outputPlaceholderKey: "network.privateSubnetIds"},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				fake := &fakeEvaluator{out: placedFunction(authored, []any{"sg-1"})}
				_, err := resolveTransforms(t.Context(), outputConfig(networkStore(nil), fake), outputManifest())
				var malformed *OutputPlaceholderError
				if !errors.As(err, &malformed) {
					t.Fatalf("resolveTransforms error = %v, want an OutputPlaceholderError", err)
				}
				if !strings.Contains(err.Error(), "fn--api--users") || !strings.Contains(err.Error(), "vpc.subnetIds") {
					t.Errorf("error = %q, want the resource and field named", err)
				}
			})
		}
	})

	t.Run("an output with no store to read from fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		fake := &fakeEvaluator{out: placedFunction(vpcPlaceholder("network", "subnetIds"), []any{"sg-1"})}
		_, err := resolveTransforms(t.Context(), Config{Env: "prod", VarsClass: "production", Transform: fake}, outputManifest())
		if err == nil {
			t.Fatal("resolveTransforms succeeded, want the unreachable store refused")
		}
		for _, fact := range []string{"fn--api--users", "network"} {
			if !strings.Contains(err.Error(), fact) {
				t.Errorf("error = %q, missing %q", err, fact)
			}
		}
	})
}

func TestMapOutputsReachesEveryPlaceholderInAField(t *testing.T) {
	t.Parallel()

	authored := map[string]any{"placement": map[string]any{"subnets": []any{vpcPlaceholder("network", "privateSubnetIds")}}}
	resolved, err := mapOutputs(authored, outputSite{Resource: "fn--api--users", Surface: "vpc", Field: "subnetIds"},
		func(ref outputRef, _ outputSite, _ any) (any, error) { return ref.Property, nil })
	if err != nil {
		t.Fatalf("mapOutputs: %v", err)
	}
	want := map[string]any{"placement": map[string]any{"subnets": []any{"privateSubnetIds"}}}
	if !reflect.DeepEqual(resolved, want) {
		t.Errorf("mapOutputs = %v, want a placeholder under an object field resolved as well", resolved)
	}
}

const vpcPlacementModule = `
	import { defineTransform } from "@ocel/provider-aws/transform"
	export default defineTransform(({ links }) => ({
		if: (ctx) => ctx.envClass === "production",
		function: {
			vpc: {
				subnetIds: links.network.subnetIds,
				securityGroupIds: links.network.securityGroupIds,
			},
		},
	}))
`

func TestVPCPlacementFromLinkOutput(t *testing.T) {
	t.Parallel()

	root := transformtest.Root(t, map[string]string{"vpc.transform.ts": vpcPlacementModule})
	manifest := outputManifest()
	store := customNetworkStore(t, map[string]any{
		"subnetIds":        []any{"subnet-0a1", "subnet-0b2"},
		"securityGroupIds": []any{"sg-0c3"},
	})
	cfg := outputConfig(store, transform.NodePass{Root: root, Modules: []string{"./vpc.transform.ts"}})

	resolved, err := resolveTransforms(t.Context(), cfg, manifest)
	if err != nil {
		t.Fatalf("resolveTransforms: %v", err)
	}

	args := resolved.forFunction(manifest.GetFunctions()[0])
	want := functionVPC{SubnetIDs: []string{"subnet-0a1", "subnet-0b2"}, SecurityGroupIDs: []string{"sg-0c3"}}
	if !reflect.DeepEqual(args.VPC, want) {
		t.Fatalf("vpc = %+v, want %+v", args.VPC, want)
	}

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, roleCoordinate("shop", testStack(t, "prod", "api")), executionRole{App: "api", VPCAccess: args.VPC.placed()})
		if err != nil {
			return err
		}
		_, err = registerFunction(pctx, "fn--api--users", functionCoordinate("shop", testStack(t, "prod", "api"), "fn--api--users"),
			"/users", args, artifactRef{Bucket: "artifacts", Key: "fn.zip"}, nil, nil, nil, nil, role.Arn)
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	config := rec.object(t, "aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90", "vpcConfig")
	if got := stringsAt(config, "subnetIds"); !reflect.DeepEqual(got, want.SubnetIDs) {
		t.Errorf("vpcConfig.subnetIds = %v, want %v", got, want.SubnetIDs)
	}
	if got := stringsAt(config, "securityGroupIds"); !reflect.DeepEqual(got, want.SecurityGroupIDs) {
		t.Errorf("vpcConfig.securityGroupIds = %v, want %v", got, want.SecurityGroupIDs)
	}
	if !slices.ContainsFunc(rec.attachments(), func(arn string) bool {
		return strings.HasSuffix(arn, "AWSLambdaVPCAccessExecutionRole")
	}) {
		t.Errorf("role policy attachments = %v, want the VPC access one among them", rec.attachments())
	}
}

const postgresSmuggleModule = `
	import { defineTransform } from "@ocel/provider-aws/transform"
	export default defineTransform(({ links }) => ({
		function: { vpc: { subnetIds: links.network.host, securityGroupIds: links.network.username } },
	}))
`

func TestVPCPlacementCannotBeSmuggledThroughPostgresProperties(t *testing.T) {
	t.Parallel()

	root := transformtest.Root(t, map[string]string{"vpc.transform.ts": postgresSmuggleModule})
	store := networkStore(&linksv1.PostgresProperties{Host: "subnet-0a1,subnet-0b2", Username: "sg-0c3"})
	cfg := outputConfig(store, transform.NodePass{Root: root, Modules: []string{"./vpc.transform.ts"}})

	_, err := resolveTransforms(t.Context(), cfg, outputManifest())
	var shape *OutputShapeError
	if !errors.As(err, &shape) {
		t.Fatalf("resolveTransforms error = %v, want the postgres host refused where a list of subnet ids belongs", err)
	}
	for _, fact := range []string{"fn--api--users", "vpc.subnetIds", "network", "host"} {
		if !strings.Contains(err.Error(), fact) {
			t.Errorf("error = %q, missing %q", err, fact)
		}
	}
}

func TestFunctionOutsideAVPCRendersNoVPCConfig(t *testing.T) {
	t.Parallel()

	args := translateFunction(&deploymentsv1.ManifestFunction{})
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, roleCoordinate("shop", testStack(t, "prod", "api")), executionRole{App: "api", VPCAccess: args.VPC.placed()})
		if err != nil {
			return err
		}
		_, err = registerFunction(pctx, "fn--api--users", functionCoordinate("shop", testStack(t, "prod", "api"), "fn--api--users"),
			"/users", args, artifactRef{Bucket: "artifacts", Key: "fn.zip"}, nil, nil, nil, nil, role.Arn)
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}
	if _, placed := rec.inputs("aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90")["vpcConfig"]; placed {
		t.Error("the function carries a vpcConfig, want none where no transform places it")
	}
	if slices.ContainsFunc(rec.attachments(), func(arn string) bool {
		return strings.HasSuffix(arn, "AWSLambdaVPCAccessExecutionRole")
	}) {
		t.Errorf("role policy attachments = %v, want no network-interface grant where nothing is placed", rec.attachments())
	}
}

func TestFunctionVPCConfigRefusesHalfAPlacement(t *testing.T) {
	t.Parallel()

	if _, err := functionVPCConfig("fn--api--users", functionVPC{SubnetIDs: []string{"subnet-a"}}); err == nil {
		t.Error("subnets with no security group were accepted, want the placement refused")
	}
	if _, err := functionVPCConfig("fn--api--users", functionVPC{SecurityGroupIDs: []string{"sg-1"}}); err == nil {
		t.Error("a security group with no subnet was accepted, want the placement refused")
	}
	if config, err := functionVPCConfig("fn--api--users", functionVPC{}); err != nil || config != nil {
		t.Errorf("functionVPCConfig with no placement = %v, %v, want no config and no error", config, err)
	}
}

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

func (r *inputRecorder) attachments() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.attached)
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
