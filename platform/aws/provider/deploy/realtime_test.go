package deploy

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	testAPIARN   = "arn:aws:appsync:us-east-1:111122223333:apis/abcdefghij"
	testHTTPHost = "abc.appsync-api.us-east-1.amazonaws.com"
)

func testRealtimeAuthorizer() payloads.Placement {
	return payloads.Placement{Bucket: "ocel-artifacts", Key: payloads.Key(realtimeAuthorizerKeyPrefix, "a0a0"), SHA256: "a0a0"}
}

func testRealtimeArgs() realtimeArgs {
	return realtimeArgs{
		Namespaces: []realtimeNamespace{
			{LogicalName: "realtime--app", Namespace: "app", VerifyKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey), SigningKeySecret: "ocel/realtime/shop/prod/realtime--app"},
			{LogicalName: "realtime--chat", Namespace: "chat", VerifyKey: ed25519.NewKeyFromSeed([]byte(strings.Repeat("c", ed25519.SeedSize))).Public().(ed25519.PublicKey), SigningKeySecret: "ocel/realtime/shop/prod/realtime--chat"},
		},
		Authorizer:  testRealtimeAuthorizer(),
		BoundaryARN: "arn:aws:iam::111122223333:policy/ocel-app-boundary",
	}
}

func registeredRealtime(t *testing.T) *tagRecorder {
	t.Helper()
	return recordTags(t, func(ctx *pulumi.Context) error {
		return registerRealtime(ctx, "shop", "prod", testRealtimeArgs())
	}, func(args pulumi.MockResourceArgs) resource.PropertyMap {
		if args.TypeToken != tokenAppSyncAPI {
			return nil
		}
		return resource.PropertyMap{
			"apiArn": resource.NewStringProperty(testAPIARN),
			"apiId":  resource.NewStringProperty("abcdefghij"),
			"dns": resource.NewObjectProperty(resource.PropertyMap{
				"HTTP":     resource.NewStringProperty(testHTTPHost),
				"REALTIME": resource.NewStringProperty("abc.appsync-realtime-api.us-east-1.amazonaws.com"),
			}),
		}
	})
}

func authTypes(t *testing.T, modes resource.PropertyValue) []string {
	t.Helper()
	var out []string
	for _, mode := range modes.ArrayValue() {
		out = append(out, mode.ObjectValue()["authType"].StringValue())
	}
	return out
}

func TestAStageServesRealtimeFromOneEventAPIThatConnectsAndSubscribesByTokenOrRoleAndPublishesByRoleAlone(t *testing.T) {
	t.Parallel()

	api := registeredRealtime(t).inputsOf(t, tokenAppSyncAPI, "realtime")
	if got := api["name"].StringValue(); got != "ocel-app-shop-prod" {
		t.Errorf("api name = %q, want ocel-app-shop-prod", got)
	}
	config := api["eventConfig"].ObjectValue()
	for field, want := range map[string][]string{
		"authProviders":             {"AWS_LAMBDA", "AWS_IAM"},
		"connectionAuthModes":       {"AWS_LAMBDA", "AWS_IAM"},
		"defaultSubscribeAuthModes": {"AWS_LAMBDA", "AWS_IAM"},
		"defaultPublishAuthModes":   {"AWS_IAM"},
	} {
		if got := authTypes(t, config[resource.PropertyKey(field)]); !slices.Equal(got, want) {
			t.Errorf("eventConfig.%s = %v, want %v", field, got, want)
		}
	}
	lambdaProvider := config["authProviders"].ArrayValue()[0].ObjectValue()["lambdaAuthorizerConfig"].ObjectValue()
	if ttl := lambdaProvider["authorizerResultTtlInSeconds"]; !ttl.IsNumber() || ttl.NumberValue() != 0 {
		t.Errorf("authorizerResultTtlInSeconds = %v, want 0 so no admission outlives its token", ttl)
	}
}

func TestEachRealtimeResourceIsAChannelNamespaceOfTheStagesAPI(t *testing.T) {
	t.Parallel()

	rec := registeredRealtime(t)
	for logical, namespace := range map[string]string{"realtime-app": "app", "realtime-chat": "chat"} {
		inputs := rec.inputsOf(t, tokenAppSyncChannelNamespace, logical)
		if got := inputs["name"].StringValue(); got != namespace {
			t.Errorf("namespace %s name = %q, want %q", logical, got, namespace)
		}
		if got := inputs["apiId"].StringValue(); got != "abcdefghij" {
			t.Errorf("namespace %s apiId = %q, want the stage's api", logical, got)
		}
	}
}

func TestTheAuthorizerIsASmallArmGoLambdaHoldingEveryNamespacesVerifyKeyAndNoSigningKey(t *testing.T) {
	t.Parallel()

	fn := registeredRealtime(t).inputsOf(t, tokenLambdaFunction, "realtime-authorizer")
	for field, want := range map[string]any{
		"runtime":    "provided.al2023",
		"handler":    "bootstrap",
		"memorySize": float64(128),
		"s3Bucket":   "ocel-artifacts",
		"s3Key":      testRealtimeAuthorizer().Key,
	} {
		if got := fn[resource.PropertyKey(field)].V; got != want {
			t.Errorf("authorizer %s = %v, want %v", field, got, want)
		}
	}
	if archs := fn["architectures"].ArrayValue(); len(archs) != 1 || archs[0].StringValue() != "arm64" {
		t.Errorf("authorizer architectures = %v, want arm64", archs)
	}
	raw := fn["environment"].ObjectValue()["variables"].ObjectValue()[awsports.RealtimeVerifyKeysEnvVar].StringValue()
	var keys map[string]string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		t.Fatalf("%s = %q, want JSON: %v", awsports.RealtimeVerifyKeysEnvVar, raw, err)
	}
	for _, ns := range testRealtimeArgs().Namespaces {
		if keys[ns.Namespace] != base64.StdEncoding.EncodeToString(ns.VerifyKey) {
			t.Errorf("verify key for %s = %q, want its public key", ns.Namespace, keys[ns.Namespace])
		}
	}
	if len(keys) != 2 {
		t.Errorf("verify keys = %v, want exactly the stage's two namespaces", keys)
	}
}

func TestOnlyTheStagesAPIMayInvokeTheAuthorizer(t *testing.T) {
	t.Parallel()

	permission := registeredRealtime(t).inputsOf(t, tokenLambdaPermission, "realtime-authorizer-permission")
	if got := permission["principal"].StringValue(); got != "appsync.amazonaws.com" {
		t.Errorf("permission principal = %q, want appsync.amazonaws.com", got)
	}
	if got := permission["sourceArn"].StringValue(); got != testAPIARN {
		t.Errorf("permission sourceArn = %q, want the stage's api alone", got)
	}
}

func TestTheAuthorizersRoleIsBoundedAndReadsItsOwnAPIAlone(t *testing.T) {
	t.Parallel()

	rec := registeredRealtime(t)
	role := rec.inputsOf(t, tokenIAMRole, "realtime-authorizer-role")
	if got := role["permissionsBoundary"].StringValue(); got != testRealtimeArgs().BoundaryARN {
		t.Errorf("role boundary = %q, want the app boundary", got)
	}
	policy := rec.inputsOf(t, tokenIAMRolePolicy, "realtime-authorizer-role-policy-api")["policy"].StringValue()
	var doc struct {
		Statement []struct {
			Action   []string
			Resource []string
		}
	}
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		t.Fatalf("policy = %q: %v", policy, err)
	}
	if len(doc.Statement) != 1 || !slices.Equal(doc.Statement[0].Action, []string{"appsync:GetApi"}) || !slices.Equal(doc.Statement[0].Resource, []string{testAPIARN}) {
		t.Errorf("policy = %s, want appsync:GetApi on the stage's api alone", policy)
	}
}

func TestEveryRealtimeResourceIsTaggedAsRealtime(t *testing.T) {
	t.Parallel()

	rec := registeredRealtime(t)
	for typeToken, cases := range map[string]map[string]naming.Kind{
		tokenAppSyncAPI:              {"realtime": naming.KindRealtime},
		tokenAppSyncChannelNamespace: {"realtime-app": naming.KindRealtime},
		tokenLambdaFunction:          {"realtime-authorizer": naming.KindRealtimeAuthorizer},
		tokenLogGroup:                {"realtime-authorizer-logs": naming.KindRealtimeAuthorizer},
	} {
		for name, kind := range cases {
			if got := rec.component(t, typeToken, name); got != kind.Component() {
				t.Errorf("%s %s component = %q, want %q", typeToken, name, got, kind.Component())
			}
		}
	}
}

func TestARealtimeStageWithoutABoundaryIsRefused(t *testing.T) {
	t.Parallel()

	args := testRealtimeArgs()
	args.BoundaryARN = ""
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return registerRealtime(ctx, "shop", "prod", args)
	}, pulumi.WithMocks("shop", "prod--infra", &tagRecorder{}))
	if err == nil || !strings.Contains(err.Error(), "boundary") {
		t.Errorf("registerRealtime() = %v, want the missing boundary refused", err)
	}
}

func TestOnlyRealtimeResourcesTheStageProvisionsGetANamespace(t *testing.T) {
	t.Parallel()

	resources := []provider.Resource{
		{Name: "realtime--app", Declared: "app", Type: provider.BindingRealtime},
		{Name: "realtime--shared", Declared: "shared", Type: provider.BindingRealtime, Binding: "elsewhere"},
		{Name: "kv--cache", Declared: "cache", Type: provider.BindingKV},
	}
	got := realtimeResourcesOf(resources)
	if len(got) != 1 || got[0].Name != "realtime--app" {
		t.Errorf("realtimeResourcesOf() = %v, want realtime--app alone", got)
	}
}
