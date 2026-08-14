package deploy

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

type fakeEvaluator struct {
	seen transform.Request
	out  []transform.Surfaces
	err  error
}

func (f *fakeEvaluator) Evaluate(_ context.Context, req transform.Request) ([]transform.Surfaces, error) {
	f.seen = req
	if f.err != nil {
		return nil, f.err
	}
	out := f.out
	if out == nil {
		out = make([]transform.Surfaces, len(req.Resources))
		for i, r := range req.Resources {
			out[i] = r.Surfaces
		}
	}
	return overTheWire(out), nil
}

func overTheWire(surfaces []transform.Surfaces) []transform.Surfaces {
	encoded, err := json.Marshal(surfaces)
	if err != nil {
		panic(err)
	}
	var decoded []transform.Surfaces
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

func transformManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug: "shop",
		Resources: []*deploymentsv1.ManifestResource{
			{
				LogicalName: "db",
				Config:      &deploymentsv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
			{
				LogicalName: "uploads",
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
		},
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "fn--api--users", App: "api"},
		},
	}
}

func TestResolveTransforms(t *testing.T) {
	t.Parallel()

	t.Run("a project naming no transforms renders the provider's own args", func(t *testing.T) {
		t.Parallel()

		manifest := transformManifest()
		resolved, err := resolveTransforms(t.Context(), Config{Env: "prod"}, manifest)
		if err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}
		if resolved != nil {
			t.Fatalf("resolved = %v, want the provider's own translation left in place", resolved)
		}

		fn := manifest.GetFunctions()[0]
		if got, want := resolved.forFunction(fn), translateFunction(fn); got != want {
			t.Errorf("forFunction = %+v, want %+v", got, want)
		}
		if got, want := resolved.forPostgres("db", manifest.GetResources()[0].GetPostgres()), translatePostgres(nil); got != want {
			t.Errorf("forPostgres = %+v, want %+v", got, want)
		}
		if got, want := resolved.forBucket("uploads", manifest.GetResources()[1].GetBucket()), translateBucket(nil); !reflect.DeepEqual(got, want) {
			t.Errorf("forBucket = %+v, want %+v", got, want)
		}
	})

	t.Run("the pass is offered every candidate with its defaulted args", func(t *testing.T) {
		t.Parallel()

		fake := &fakeEvaluator{}
		cfg := Config{Env: "prod", Transform: fake}
		if _, err := resolveTransforms(t.Context(), cfg, transformManifest()); err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}

		if fake.seen.EnvClass != "production" || fake.seen.Env != "prod" {
			t.Errorf("ambient context = %q/%q, want production/prod", fake.seen.EnvClass, fake.seen.Env)
		}
		if len(fake.seen.Resources) != 3 {
			t.Fatalf("candidates = %d, want the postgres, the bucket and the function", len(fake.seen.Resources))
		}

		byName := map[string]transform.Resource{}
		for _, r := range fake.seen.Resources {
			byName[r.Name] = r
		}
		if got := byName["fn--api--users"]; got.Type != "function" || got.App != "api" {
			t.Errorf("function candidate = %+v, want type function owned by api", got)
		}
		if got := byName["fn--api--users"].Surfaces["lambda"]["memorySizeMb"]; got != defaultFunctionMemoryMB {
			t.Errorf("lambda.memorySizeMb = %v, want the provider's default %d", got, defaultFunctionMemoryMB)
		}
		if got := byName["fn--api--users"].Surfaces["url"]["invokeMode"]; got != functionURLInvokeModeStream {
			t.Errorf("url.invokeMode = %v, want %q", got, functionURLInvokeModeStream)
		}
		if got := byName["db"]; got.Type != "postgres" || got.Surfaces["cluster"]["engineVersion"] != defaultPostgresEngineVersion {
			t.Errorf("postgres candidate = %+v, want the defaulted cluster args", got)
		}
		if got := byName["uploads"]; got.Type != "bucket" || got.Surfaces["listener"]["timeoutSeconds"] != listenerTimeoutSeconds {
			t.Errorf("bucket candidate = %+v, want the defaulted listener args", got)
		}
	})

	t.Run("a resource shared across apps is offered with no owning app", func(t *testing.T) {
		t.Parallel()

		fake := &fakeEvaluator{}
		if _, err := resolveTransforms(t.Context(), Config{Env: "prod", Transform: fake}, transformManifest()); err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}

		for _, r := range fake.seen.Resources {
			if r.Type != "function" && r.App != "" {
				t.Errorf("%s was offered as owned by %q, want no owning app", r.Name, r.App)
			}
		}
	})

	t.Run("the evaluated surfaces land on the args the provider renders", func(t *testing.T) {
		t.Parallel()

		manifest := transformManifest()
		fake := &fakeEvaluator{out: []transform.Surfaces{
			{
				"cluster": {
					"engineVersion":      "16.6",
					"minCapacity":        float64(2),
					"maxCapacity":        float64(8),
					"deletionProtection": true,
					"skipFinalSnapshot":  false,
				},
				"instance": {"instanceClass": "db.r6g.large", "publiclyAccessible": false},
			},
			{
				"bucket": {"forceDestroy": true},
				"cors": {
					"allowedOrigins": []any{"https://shop.example"},
					"allowedMethods": []any{"PUT", "POST"},
					"allowedHeaders": []any{"*"},
					"exposeHeaders":  []any{"ETag"},
					"maxAgeSeconds":  float64(60),
				},
				"listener":     {"timeoutSeconds": float64(90)},
				"notification": {"events": []any{"s3:ObjectCreated:Put"}},
			},
			{
				"lambda": {"memorySizeMb": float64(2048), "timeoutSeconds": float64(60), "runtime": "nodejs22.x"},
				"url":    {"invokeMode": "BUFFERED"},
			},
		}}

		resolved, err := resolveTransforms(t.Context(), Config{Env: "prod", Transform: fake}, manifest)
		if err != nil {
			t.Fatalf("resolveTransforms: %v", err)
		}

		fn := resolved.forFunction(manifest.GetFunctions()[0])
		if fn.MemorySizeMB != 2048 || fn.TimeoutSeconds != 60 || fn.Runtime != "nodejs22.x" || fn.InvokeMode != "BUFFERED" {
			t.Errorf("function args = %+v, want the transformed lambda and url args", fn)
		}

		db := resolved.forPostgres("db", manifest.GetResources()[0].GetPostgres())
		if db.EngineVersion != "16.6" || db.MaxCapacity != 8 || !db.DeletionProtection || db.SkipFinalSnapshot {
			t.Errorf("postgres args = %+v, want the transformed cluster args", db)
		}
		if db.InstanceClass != "db.r6g.large" {
			t.Errorf("instanceClass = %q, want the transformed instance class", db.InstanceClass)
		}
		if db.MasterUsername != postgresMasterUsername || db.Port != postgresPort {
			t.Errorf("postgres args = %+v, want the untransformable fields left alone", db)
		}

		bucket := resolved.forBucket("uploads", manifest.GetResources()[1].GetBucket())
		if !bucket.ForceDestroy || bucket.ListenerTimeoutSeconds != 90 {
			t.Errorf("bucket args = %+v, want the transformed bucket and listener args", bucket)
		}
		if !reflect.DeepEqual(bucket.CORS.AllowedOrigins, []string{"https://shop.example"}) {
			t.Errorf("cors.allowedOrigins = %v, want the transformed origins", bucket.CORS.AllowedOrigins)
		}
		if !reflect.DeepEqual(bucket.AllowedOrigins, bucket.CORS.AllowedOrigins) {
			t.Errorf("the listener advertises %v while CORS allows %v", bucket.AllowedOrigins, bucket.CORS.AllowedOrigins)
		}
		if !reflect.DeepEqual(bucket.NotificationEvents, []string{"s3:ObjectCreated:Put"}) {
			t.Errorf("notification.events = %v, want the transformed events", bucket.NotificationEvents)
		}
	})

	t.Run("a surface returned with the wrong shape fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		manifest := &deploymentsv1.Manifest{
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "fn--api--users", App: "api"}},
		}
		fake := &fakeEvaluator{out: []transform.Surfaces{{
			"lambda": {"memorySizeMb": "plenty", "timeoutSeconds": float64(30), "runtime": "nodejs24.x"},
			"url":    {"invokeMode": "BUFFERED"},
		}}}

		_, err := resolveTransforms(t.Context(), Config{Env: "prod", Transform: fake}, manifest)
		if err == nil {
			t.Fatal("resolveTransforms succeeded, want the mistyped field rejected")
		}
		for _, fact := range []string{"fn--api--users", "lambda.memorySizeMb"} {
			if !strings.Contains(err.Error(), fact) {
				t.Errorf("error = %q, missing %q", err, fact)
			}
		}
	})
}
