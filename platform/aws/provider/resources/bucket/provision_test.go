package bucket

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	testBucket  = "ocel-app-shop-prod-uploads"
	tokenS3ABAC = "aws:s3/bucketAbac:BucketAbac"
)

func testStack(project string) component.Stack {
	return component.Stack{
		Ref:            provider.StackRef{Tier: environment.TierProduction},
		Namespace:      "ocel",
		Project:        project,
		Env:            "prod",
		StateTable:     "ocel-state",
		AppBoundaryARN: "arn:aws:iam::111122223333:policy/ocel-app-boundary",
		AppRolePath:    componenttest.AppRolePath,
	}
}

func testSessions(project string) Sessions {
	return NewSessions(project, "prod", "arn:aws:dynamodb:eu-west-1:111122223333:table/ocel-state")
}

func testUploadCompleter() payloads.Placement {
	return payloads.Placement{
		Bucket: "ocel-artifacts",
		Key:    payloads.Key(UploadCompleterPayload, "d0d0"),
		SHA256: "d0d0",
	}
}

func TestBucketComponentTags(t *testing.T) {
	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		err := register(ctx, testStack("shop"), "bucket--uploads", translate(&provider.BucketSpec{}), testSessions("shop"), testUploadCompleter())
		return err
	})

	cases := []struct {
		typeToken string
		name      string
		want      string
	}{
		{"aws:s3/bucketV2:BucketV2", testBucket, naming.KindBucket.Component()},
		{"aws:iam/role:Role", testBucket + "-completer", naming.KindRole.Component()},
		{"aws:lambda/function:Function", testBucket + "-completer", naming.KindUploadCompleter.Component()},
	}
	for _, tc := range cases {
		if got := rec.Component(t, tc.typeToken, tc.name); got != tc.want {
			t.Errorf("%s on %s = %q, want %q", component.TagComponent, tc.name, got, tc.want)
		}
	}
}

func TestBucketUploadCompleterDescriptionFitsLambda(t *testing.T) {
	long := strings.Repeat("storefront-", 40)
	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack(long), "bucket--uploads", translate(&provider.BucketSpec{}), testSessions(long), testUploadCompleter())
	})

	head, err := component.UntaggedName("ocel", component.ResourceCoordinate(long, "prod", "bucket--uploads", naming.KindBucket), component.MaxLambdaNameLen, uploadCompleterSuffix)
	if err != nil {
		t.Fatal(err)
	}
	got := rec.InputsOf(t, "aws:lambda/function:Function", head+"-completer")["description"].StringValue()
	if len(got) != component.MaxDescriptionLen {
		t.Errorf("upload completer description is %d chars, want it clamped to the %d Lambda accepts", len(got), component.MaxDescriptionLen)
	}
}

func TestABucketIsNamedUnderTheAppFenceWithTheRandomTagItsResourceMintedAndItsCompleterBesideIt(t *testing.T) {
	t.Parallel()

	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop"), "bucket--uploads", translate(&provider.BucketSpec{}), testSessions("shop"), testUploadCompleter())
	})

	physical := testBucket + "-" + componenttest.RandomTag
	if !rec.IsRegistered(component.RandomTagToken, testBucket+"/tag") {
		t.Fatalf("no random tag was registered for the bucket, so its name would change on every deploy")
	}
	if got := rec.InputsOf(t, component.TokenS3Bucket, testBucket)["bucket"]; !got.IsString() || got.StringValue() != physical {
		t.Errorf("bucket = %v, want %q", got, physical)
	}
	for _, token := range []string{component.TokenLambdaFunction, component.TokenIAMRole} {
		if got := rec.InputsOf(t, token, testBucket+"-completer")["name"]; !got.IsString() || got.StringValue() != physical+"-completer" {
			t.Errorf("%s name = %v, want %q", token, got, physical+"-completer")
		}
	}
	if got := rec.InputsOf(t, component.TokenLogGroup, "/aws/lambda/"+testBucket+"-completer")["name"]; !got.IsString() || got.StringValue() != "/aws/lambda/"+physical+"-completer" {
		t.Errorf("completer log group name = %v, want the group Lambda writes the completer's logs to", got)
	}
}

func TestABucketIsCreatedTaggedForItsNamespaceAndTierWithABACOnThatOutlivesIt(t *testing.T) {
	t.Parallel()

	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop"), "bucket--uploads", translate(&provider.BucketSpec{AllowedOrigins: []string{"https://app.example.com"}}), testSessions("shop"), testUploadCompleter())
	})

	tags := rec.Tags(t, component.TokenS3Bucket, testBucket)
	if tags[naming.ManagedByTagKey] != "ocel" || tags[naming.EnvTierTagKey] != string(environment.TierProduction) {
		t.Errorf("bucket tags = %v, want the namespace and tier the deploy credential creates a bucket under", tags)
	}
	abac := rec.InputsOf(t, tokenS3ABAC, testBucket+"/abac")
	if got := abac["abacStatus"].ObjectValue()["status"]; got.StringValue() != "Enabled" {
		t.Errorf("abacStatus = %v, want Enabled: the deploy credential's bucket grants hold only on a bucket that evaluates its tags", got)
	}
	if !rec.RetainsOnDelete(tokenS3ABAC, testBucket+"/abac") {
		t.Error("deleting the ABAC setting disables ABAC first, after which the deploy credential may no longer delete the bucket")
	}
	for token, name := range map[string]string{
		component.TokenS3PublicAccess: testBucket + "/public-access-block",
		component.TokenS3CORS:         testBucket + "/cors",
		component.TokenS3Notification: testBucket + "/notification",
	} {
		if !slices.Contains(rec.DependenciesOf(token, name), "urn:pulumi:prod--infra::shop::"+tokenS3ABAC+"::"+testBucket+"/abac") {
			t.Errorf("%s %s does not wait for ABAC, so its call reaches a bucket whose tags are not yet evaluated", token, name)
		}
	}
}

func TestABucketIsEmptiedOfItsObjectsWhenItIsDeleted(t *testing.T) {
	t.Parallel()

	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop"), "bucket--uploads", translate(&provider.BucketSpec{}), testSessions("shop"), testUploadCompleter())
	})

	if got := rec.InputsOf(t, component.TokenS3Bucket, testBucket)["forceDestroy"]; !got.IsBool() || !got.BoolValue() {
		t.Errorf("forceDestroy = %v, want true: S3 refuses to delete a bucket that holds an object, so destroying the stack would stop partway", got)
	}
}

func TestResourceCoordinate(t *testing.T) {
	t.Parallel()

	t.Run("takes the local name from the logical name's fields", func(t *testing.T) {
		t.Parallel()

		at := component.ResourceCoordinate("shop", "prod", "bucket--uploads", naming.KindBucket)
		if at.Name != "uploads" {
			t.Errorf("Name = %q, want %q", at.Name, "uploads")
		}
		if at.App != naming.InfraApp {
			t.Errorf("App = %q, want %q — resources live on the environment's infra stack", at.App, naming.InfraApp)
		}
	})

	t.Run("an unqualified logical name is its own local name", func(t *testing.T) {
		t.Parallel()

		at := component.ResourceCoordinate("shop", "prod", "uploads", naming.KindBucket)
		if at.Name != "uploads" {
			t.Errorf("Name = %q, want %q", at.Name, "uploads")
		}
	})
}

func TestBucketDescriptions(t *testing.T) {
	t.Parallel()

	at := component.ResourceCoordinate("shop", "prod", "bucket--uploads", naming.KindBucket)
	got := at.Description("upload completer for the " + at.Name + " bucket")
	if !strings.HasPrefix(got, "shop / prod / infra") {
		t.Errorf("Description() = %q, want it to open with the coordinate", got)
	}
	if !strings.Contains(got, "uploads") {
		t.Errorf("Description() = %q, want it to name the bucket", got)
	}
}

func TestTranslateBucket(t *testing.T) {
	t.Parallel()

	t.Run("CORS from allowed origins", func(t *testing.T) {
		t.Parallel()

		origins := []string{"https://app.example.com", "https://www.example.com"}
		got := translate(&provider.BucketSpec{AllowedOrigins: origins})

		if !reflect.DeepEqual(got.AllowedOrigins, origins) {
			t.Errorf("AllowedOrigins = %v, want %v (passed through for the upload completer allowlist)", got.AllowedOrigins, origins)
		}
		if !reflect.DeepEqual(got.CORS.AllowedOrigins, origins) {
			t.Errorf("CORS.AllowedOrigins = %v, want the app's declared origins %v", got.CORS.AllowedOrigins, origins)
		}
		if !slices.Contains(got.CORS.AllowedMethods, "PUT") {
			t.Errorf("CORS.AllowedMethods = %v, want it to permit browser PUT", got.CORS.AllowedMethods)
		}
		if len(got.CORS.AllowedHeaders) == 0 {
			t.Error("CORS.AllowedHeaders is empty; the presigned PUT's signed headers need a preflight allow")
		}
		if got.CORS.MaxAgeSeconds != bucketCORSMaxAgeSeconds {
			t.Errorf("CORS.MaxAgeSeconds = %d, want %d", got.CORS.MaxAgeSeconds, bucketCORSMaxAgeSeconds)
		}
	})

	t.Run("notification and lambda args", func(t *testing.T) {
		t.Parallel()

		got := translate(&provider.BucketSpec{})

		if !reflect.DeepEqual(got.NotificationEvents, []string{"s3:ObjectCreated:*"}) {
			t.Errorf("NotificationEvents = %v, want [s3:ObjectCreated:*]", got.NotificationEvents)
		}
		if got.UploadCompleterRuntime != uploadCompleterRuntime {
			t.Errorf("UploadCompleterRuntime = %q, want %q (Go custom runtime)", got.UploadCompleterRuntime, uploadCompleterRuntime)
		}
		if got.UploadCompleterHandler != uploadCompleterHandler {
			t.Errorf("UploadCompleterHandler = %q, want %q", got.UploadCompleterHandler, uploadCompleterHandler)
		}
		if got.UploadCompleterTimeoutSeconds != uploadCompleterTimeoutSeconds {
			t.Errorf("UploadCompleterTimeoutSeconds = %d, want %d", got.UploadCompleterTimeoutSeconds, uploadCompleterTimeoutSeconds)
		}
	})

	t.Run("IAM args", func(t *testing.T) {
		t.Parallel()

		got := translate(&provider.BucketSpec{})

		if !slices.Contains(got.UploadCompleterS3Actions, "s3:GetObjectTagging") {
			t.Errorf("UploadCompleterS3Actions = %v, want it to include s3:GetObjectTagging", got.UploadCompleterS3Actions)
		}
		if slices.Contains(got.UploadCompleterS3Actions, "s3:PutObject") {
			t.Errorf("UploadCompleterS3Actions = %v, must not grant s3:PutObject (least privilege)", got.UploadCompleterS3Actions)
		}
		if !slices.Contains(got.UploadCompleterSessionActions, "dynamodb:UpdateItem") {
			t.Errorf("UploadCompleterSessionActions = %v, want it to include dynamodb:UpdateItem (transition)", got.UploadCompleterSessionActions)
		}
	})

	t.Run("empty origins yield empty CORS origins", func(t *testing.T) {
		t.Parallel()

		got := translate(&provider.BucketSpec{})
		if len(got.CORS.AllowedOrigins) != 0 {
			t.Errorf("CORS.AllowedOrigins = %v, want empty for a bucket with no declared origins", got.CORS.AllowedOrigins)
		}
	})
}

func TestSessionStatement(t *testing.T) {
	t.Parallel()

	const tableARN = "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state"

	t.Run("reaches only the app's own session keys", func(t *testing.T) {
		t.Parallel()

		sessions := NewSessions("shop", "prod", tableARN)
		stmt := sessionStatement([]string{"dynamodb:Query"}, sessions)
		doc, err := iam.InlinePolicy(stmt.Actions, []string{tableARN}, stmt.Condition)
		if err != nil {
			t.Fatalf("inlinePolicy: %v", err)
		}

		var parsed struct {
			Statement []struct {
				Condition map[string]map[string][]string
			}
		}
		if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
			t.Fatalf("unmarshal policy: %v", err)
		}
		keys := parsed.Statement[0].Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
		if !reflect.DeepEqual(keys, []string{sessions.KeyPrefix + "*"}) {
			t.Fatalf("LeadingKeys = %v, want %q — a role must reach neither the stack index nor another deploy's sessions", keys, sessions.KeyPrefix+"*")
		}
	})

	t.Run("an unconditioned statement has no condition", func(t *testing.T) {
		t.Parallel()

		doc, err := iam.InlinePolicy([]string{"s3:PutObject"}, []string{"arn:aws:s3:::b/*"}, nil)
		if err != nil {
			t.Fatalf("inlinePolicy: %v", err)
		}
		if strings.Contains(doc, "Condition") {
			t.Errorf("policy = %s, want no empty Condition block", doc)
		}
	})
}

func TestBucketCORSFollowsTheDeclaredOrigins(t *testing.T) {
	const corsToken = "aws:s3/bucketCorsConfigurationV2:BucketCorsConfigurationV2"

	registered := func(t *testing.T, spec *provider.BucketSpec) bool {
		t.Helper()
		rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
			return register(ctx, testStack("shop"), "bucket--uploads", translate(spec), testSessions("shop"), testUploadCompleter())
		})
		return rec.IsRegistered(corsToken, testBucket+"/cors")
	}

	t.Run("a bucket declaring no origins gets no CORS configuration", func(t *testing.T) {
		if registered(t, &provider.BucketSpec{}) {
			t.Error("a CORS configuration with no allowed origins was registered; S3 rejects one, so the deploy fails on every account")
		}
	})

	t.Run("a bucket declaring origins gets one", func(t *testing.T) {
		if !registered(t, &provider.BucketSpec{AllowedOrigins: []string{"https://app.example.com"}}) {
			t.Error("no CORS configuration was registered for a bucket that declares an allowed origin")
		}
	})
}

func TestBucketUploadCompleterLogGroup(t *testing.T) {
	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop"), "bucket--uploads", translate(&provider.BucketSpec{}), testSessions("shop"), testUploadCompleter())
	})

	group := rec.InputsOf(t, "aws:cloudwatch/logGroup:LogGroup", "/aws/lambda/"+testBucket+"-completer")
	retention, ok := group["retentionInDays"]
	if !ok || !retention.IsNumber() || retention.NumberValue() <= 0 {
		t.Errorf("the upload completer's log group retentionInDays = %v, want a finite retention rather than events kept forever", retention)
	}

	logging := rec.InputsOf(t, "aws:lambda/function:Function", testBucket+"-completer")["loggingConfig"]
	if !logging.IsObject() || logging.ObjectValue()["logGroup"].StringValue() != group["name"].StringValue() {
		t.Errorf("the upload completer's loggingConfig = %v, want it pointed at the group the deploy owns", logging)
	}
}

func TestABucketWithNoBoundaryIsRefusedRatherThanMintedUncapped(t *testing.T) {
	t.Parallel()

	program := func(ctx *pulumi.Context) error {
		unbounded := testStack("shop")
		unbounded.AppBoundaryARN = ""
		return register(ctx, unbounded, "bucket--uploads", translate(&provider.BucketSpec{}), testSessions("shop"), testUploadCompleter())
	}
	err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--infra", &componenttest.Recorder{}))
	if err == nil || !strings.Contains(err.Error(), "boundary") {
		t.Fatalf("register with no boundary = %v, want a refusal naming the boundary", err)
	}
}
