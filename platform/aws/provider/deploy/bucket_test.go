package deploy

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestBucketResourceIDs(t *testing.T) {
	t.Parallel()

	at := resourceCoordinate("shop", "prod", "bucket--uploads", naming.KindBucket)

	cases := map[string]string{
		"bucket-uploads":                            naming.ResourceID(at.Kind, at.Name),
		"bucket-uploads-public-access-block":        naming.ResourceID(at.Kind, at.Name, "public-access-block"),
		"bucket-uploads-cors":                       naming.ResourceID(at.Kind, at.Name, "cors"),
		"bucket-uploads-runtime-role":               naming.ResourceID(at.Kind, at.Name, "runtime-role"),
		"bucket-uploads-event-listener":             naming.ResourceID(at.Kind, at.Name, "event-listener"),
		"bucket-uploads-event-listener-permission":  naming.ResourceID(at.Kind, at.Name, "event-listener-permission"),
		"bucket-uploads-notification":               naming.ResourceID(at.Kind, at.Name, "notification"),
		"bucket-uploads-event-listener-logs-policy": naming.ResourceID(at.Kind, at.Name, "event-listener-logs-policy"),
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("resource id = %q, want %q", got, want)
		}
		if strings.Contains(got, "_") {
			t.Errorf("resource id %q mixes alphabets; the deploy log is kebab throughout", got)
		}
	}
}

func TestBucketPhysicalPrefix(t *testing.T) {
	t.Parallel()

	t.Run("carries project, env and resource", func(t *testing.T) {
		t.Parallel()

		at := resourceCoordinate("shop", "prod", "bucket--uploads", naming.KindBucket)
		if got, want := at.PhysicalPrefix(maxS3BucketPrefixLen), "shop-prod-uploads-"; got != want {
			t.Errorf("PhysicalPrefix() = %q, want %q", got, want)
		}
	})

	t.Run("fits the name AWS appends to", func(t *testing.T) {
		t.Parallel()

		at := resourceCoordinate(strings.Repeat("p", 30), strings.Repeat("e", 30), "bucket--"+strings.Repeat("u", 30), naming.KindBucket)
		got := at.PhysicalPrefix(maxS3BucketPrefixLen)
		if len(got) > maxS3BucketPrefixLen {
			t.Errorf("PhysicalPrefix() = %q, length %d, want <= %d", got, len(got), maxS3BucketPrefixLen)
		}
		if len(got)+s3AutonameSuffixLen > maxS3BucketNameLen {
			t.Errorf("PhysicalPrefix() length %d leaves no room for the %d-character suffix within %d", len(got), s3AutonameSuffixLen, maxS3BucketNameLen)
		}
		for _, r := range got {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				t.Fatalf("PhysicalPrefix() = %q, contains %q, which S3 rejects", got, r)
			}
		}
		if first := got[0]; !(first >= 'a' && first <= 'z') && !(first >= '0' && first <= '9') {
			t.Errorf("PhysicalPrefix() = %q, want a letter or digit first", got)
		}
	})

	t.Run("two long names sharing a prefix stay distinct", func(t *testing.T) {
		t.Parallel()

		shared := strings.Repeat("uploads-", 6)
		a := resourceCoordinate("shop", "prod", "bucket--"+shared+"alpha", naming.KindBucket).PhysicalPrefix(maxS3BucketPrefixLen)
		b := resourceCoordinate("shop", "prod", "bucket--"+shared+"beta", naming.KindBucket).PhysicalPrefix(maxS3BucketPrefixLen)
		if a == b {
			t.Errorf("PhysicalPrefix() collided: both %q", a)
		}
	})
}

func TestResourceCoordinate(t *testing.T) {
	t.Parallel()

	t.Run("takes the local name from the logical name's fields", func(t *testing.T) {
		t.Parallel()

		at := resourceCoordinate("shop", "prod", "bucket--uploads", naming.KindBucket)
		if at.Name != "uploads" {
			t.Errorf("Name = %q, want %q", at.Name, "uploads")
		}
		if at.App != naming.InfraApp {
			t.Errorf("App = %q, want %q — resources live on the environment's infra stack", at.App, naming.InfraApp)
		}
	})

	t.Run("an unqualified logical name is its own local name", func(t *testing.T) {
		t.Parallel()

		at := resourceCoordinate("shop", "prod", "uploads", naming.KindBucket)
		if at.Name != "uploads" {
			t.Errorf("Name = %q, want %q", at.Name, "uploads")
		}
	})
}

func TestBucketDescriptions(t *testing.T) {
	t.Parallel()

	at := resourceCoordinate("shop", "prod", "bucket--uploads", naming.KindBucket)
	got := at.Description("upload event listener for the " + at.Name + " bucket")
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
		got := translateBucket(&resourcesv1.BucketConfig{AllowedOrigins: origins})

		if !reflect.DeepEqual(got.AllowedOrigins, origins) {
			t.Errorf("AllowedOrigins = %v, want %v (carried through for the listener allowlist)", got.AllowedOrigins, origins)
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

		got := translateBucket(&resourcesv1.BucketConfig{})

		if !reflect.DeepEqual(got.NotificationEvents, []string{"s3:ObjectCreated:*"}) {
			t.Errorf("NotificationEvents = %v, want [s3:ObjectCreated:*]", got.NotificationEvents)
		}
		if got.ListenerRuntime != listenerRuntime {
			t.Errorf("ListenerRuntime = %q, want %q (Go custom runtime)", got.ListenerRuntime, listenerRuntime)
		}
		if got.ListenerHandler != listenerHandler {
			t.Errorf("ListenerHandler = %q, want %q", got.ListenerHandler, listenerHandler)
		}
		if got.ListenerTimeoutSeconds != listenerTimeoutSeconds {
			t.Errorf("ListenerTimeoutSeconds = %d, want %d", got.ListenerTimeoutSeconds, listenerTimeoutSeconds)
		}
	})

	t.Run("IAM args", func(t *testing.T) {
		t.Parallel()

		got := translateBucket(&resourcesv1.BucketConfig{})

		if !slices.Contains(got.RuntimeS3Actions, "s3:PutObject") {
			t.Errorf("RuntimeS3Actions = %v, want it to include s3:PutObject (presign)", got.RuntimeS3Actions)
		}
		for _, want := range []string{"dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem"} {
			if !slices.Contains(got.RuntimeSessionActions, want) {
				t.Errorf("RuntimeSessionActions = %v, want it to include %q", got.RuntimeSessionActions, want)
			}
		}

		if !slices.Contains(got.ListenerS3Actions, "s3:GetObjectTagging") {
			t.Errorf("ListenerS3Actions = %v, want it to include s3:GetObjectTagging", got.ListenerS3Actions)
		}
		if slices.Contains(got.ListenerS3Actions, "s3:PutObject") {
			t.Errorf("ListenerS3Actions = %v, must not grant s3:PutObject (least privilege)", got.ListenerS3Actions)
		}
		if !slices.Contains(got.ListenerSessionActions, "dynamodb:UpdateItem") {
			t.Errorf("ListenerSessionActions = %v, want it to include dynamodb:UpdateItem (transition)", got.ListenerSessionActions)
		}
	})

	t.Run("empty origins yield empty CORS origins", func(t *testing.T) {
		t.Parallel()

		got := translateBucket(&resourcesv1.BucketConfig{})
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

		stmt := sessionStatement([]string{"dynamodb:Query"}, tableARN)
		doc, err := inlinePolicy(stmt.Actions, []string{tableARN}, stmt.Condition)
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
		if !reflect.DeepEqual(keys, []string{sessionKeyPrefix + "*"}) {
			t.Fatalf("LeadingKeys = %v, want %q — an app must not read or write the stack index", keys, sessionKeyPrefix+"*")
		}
	})

	t.Run("an unconditioned statement carries no condition", func(t *testing.T) {
		t.Parallel()

		doc, err := inlinePolicy([]string{"s3:PutObject"}, []string{"arn:aws:s3:::b/*"}, nil)
		if err != nil {
			t.Fatalf("inlinePolicy: %v", err)
		}
		if strings.Contains(doc, "Condition") {
			t.Errorf("policy = %s, want no empty Condition block", doc)
		}
	})
}
