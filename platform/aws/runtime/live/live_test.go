package live

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

type sink struct {
	mu    sync.Mutex
	lines []string
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

type fixedSource map[string]string

func (f fixedSource) Fetch(context.Context) (map[string]string, error) { return f, nil }

func unsetAWS(t *testing.T) {
	t.Helper()
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_ENDPOINT_URL"} {
		t.Setenv(name, "")
	}
}

func plant(t *testing.T, raw []byte) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, vars.FilePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func postgresBinding() live.Binding {
	return live.Binding{
		Name: "db--main",
		Key:  "OCEL_RESOURCE_POSTGRES_main",
		Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES,
	}
}

func decodeBinding(t *testing.T, raw string) *bindingsv1.Binding {
	t.Helper()
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal([]byte(raw), binding); err != nil {
		t.Fatalf("the child was handed %q, which it cannot parse: %v", raw, err)
	}
	return binding
}

func TestResolveLiveValues(t *testing.T) {
	t.Run("a function with no manifest builds nothing", func(t *testing.T) {
		root := t.TempDir()
		unsetAWS(t)

		l, err := Resolve(context.Background(), root)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l != nil {
			t.Fatal("a function with no live manifest built a store client anyway")
		}

		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Errorf("start/join on a function with no live values = %v, want nil", err)
		}
		l.Attach(&sink{})
		l.Refresh(context.Background())
	})

	t.Run("addresses each binding by the partition its pair lives in", func(t *testing.T) {
		manifest := vars.Manifest{
			Slug:        "shop",
			Table:       "ocel-vars",
			KeyARN:      "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:       "preview",
			Environment: "pr-42",
			Bindings: []live.Binding{
				{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main"},
				{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads"},
			},
		}

		names := bindingNames(manifest.Bindings)
		if len(names) != 2 {
			t.Fatalf("names = %v, want one per binding", names)
		}
		for i, want := range manifest.Bindings {
			if names[i] != want.Name {
				t.Errorf("name %q, want the binding %q the record is published under", names[i], want.Name)
			}
		}

		resolved := merged(nil, manifest.Bindings, []values.Published{
			publishedRecord(t, &bindingsv1.Binding{Name: "db--main", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "h", Database: "d", Username: "u"}}}, 1),
			publishedRecord(t, &bindingsv1.Binding{Name: "bucket--uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-uploads"}}}, 1),
		})
		for _, l := range manifest.Bindings {
			if resolved[l.Key] == "" {
				t.Errorf("%s reached the child under no key; the record is filed under the key the app reads", l.Name)
			}
		}

		keys := live.Keys(manifest.Keys, manifest.Bindings)
		if len(keys) != 2 {
			t.Fatalf("declared keys = %v, want the binding keys named to the child", keys)
		}
	})

	t.Run("reads the pinned coordinates and never the sentinels", func(t *testing.T) {
		manifest := vars.Manifest{
			Slug:   "shop",
			Table:  "ocel-vars",
			KeyARN: "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:  "production",
			Keys:   []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		}

		cells := manifestCells(manifest)
		if len(cells) != 2 {
			t.Fatalf("cells = %+v, want one per pinned key", cells)
		}
		if cells[0].Folder != "" {
			t.Errorf("root key folder = %q, want empty: the store owns the root sentinel", cells[0].Folder)
		}
		if cells[1].Folder != "/web" {
			t.Errorf("scoped key folder = %q, want the pinned folder", cells[1].Folder)
		}
	})

	t.Run("a preview names each cell once and reads its environment through the reader", func(t *testing.T) {
		manifest := vars.Manifest{
			Slug:        "shop",
			Environment: "pr-42",
			Keys:        []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		}

		want := []values.Cell{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}}
		if cells := manifestCells(manifest); !reflect.DeepEqual(cells, want) {
			t.Errorf("cells = %+v, want %+v: the override is the reader's environment, not a cell of its own", cells, want)
		}
	})

	t.Run("production asks for one cell per key", func(t *testing.T) {
		cells := manifestCells(vars.Manifest{
			Slug: "shop",
			Keys: []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		})

		if len(cells) != 2 {
			t.Fatalf("cells = %+v, want one per pinned key", cells)
		}
	})

	t.Run("an unreadable manifest is an init failure", func(t *testing.T) {
		cases := map[string]func(t *testing.T, path string){
			"will not parse": func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			"cannot be read at all": func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		}

		for name, sabotage := range cases {
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, vars.FilePath)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				sabotage(t, path)

				if _, err := Resolve(context.Background(), root); err == nil {
					t.Fatal("Resolve absorbed a manifest it could not read")
				}
			})
		}
	})

	t.Run("a manifest naming no keys builds nothing", func(t *testing.T) {
		root := plant(t, []byte(`{"slug":"shop","table":"ocel-vars","keyArn":"arn","class":"production","keys":[]}`))
		unsetAWS(t)

		l, err := Resolve(context.Background(), root)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l != nil {
			t.Fatal("a manifest naming no keys built a store client anyway")
		}
	})

	t.Run("declares exactly the pinned keys", func(t *testing.T) {
		rendered, err := vars.Render(vars.Manifest{
			Slug:   "shop",
			Table:  "ocel-vars",
			KeyARN: "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:  "production",
			Keys:   []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		root := plant(t, rendered)
		t.Setenv("AWS_REGION", "us-east-1")

		l, err := Resolve(context.Background(), root)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		got := l.Env()
		if len(got) != 1 {
			t.Fatalf("Env() = %q, want exactly one entry", got)
		}
		if got[0] != "OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET" {
			t.Errorf("Env() = %q, want the pinned keys by bare name, in manifest order", got[0])
		}
		if strings.Contains(got[0], "/web") {
			t.Errorf("Env() = %q, which leaks a folder into the runtime", got[0])
		}
	})

	t.Run("reads the same manifest from the file and from the bytes a container is handed", func(t *testing.T) {
		binding := postgresBinding()
		rendered, err := vars.Render(vars.Manifest{
			Slug:        "shop",
			Table:       "ocel-vars",
			KeyARN:      "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:       "preview",
			Environment: "pr-42",
			Keys:        []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
			Bindings:    []live.Binding{binding},
		})
		if err != nil {
			t.Fatal(err)
		}
		root := plant(t, rendered)
		t.Setenv("AWS_REGION", "us-east-1")

		fromFile, err := Resolve(context.Background(), root)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		fromBytes, err := FromManifest(context.Background(), rendered)
		if err != nil {
			t.Fatalf("FromManifest: %v", err)
		}

		if !slices.Equal(fromBytes.Keys(), fromFile.Keys()) {
			t.Errorf("FromManifest declares %q, want the %q the file declares", fromBytes.Keys(), fromFile.Keys())
		}
		if !slices.Equal(fromBytes.Env(), fromFile.Env()) {
			t.Errorf("FromManifest names %q to the child, want %q", fromBytes.Env(), fromFile.Env())
		}
		if !slices.Equal(fromBytes.Bindings(), fromFile.Bindings()) {
			t.Errorf("FromManifest carries %+v, want %+v", fromBytes.Bindings(), fromFile.Bindings())
		}
	})
}

func TestMerged(t *testing.T) {
	bindings := []live.Binding{{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main"}}
	published := &bindingsv1.Binding{
		Name:       "db--main",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "ocel", Port: 5432}},
	}
	records := []values.Published{publishedRecord(t, published, 1)}

	t.Run("a binding is never shadowed by a secret that shares its name", func(t *testing.T) {
		got := merged(map[string]string{"OCEL_RESOURCE_POSTGRES_main": "postgres://mine"}, bindings, records)
		if handed := decodeBinding(t, got["OCEL_RESOURCE_POSTGRES_main"]); !proto.Equal(handed, published) {
			t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want the record ocel published for the binding; a secret the user named the same way must not stand in for a resource's own credential", got["OCEL_RESOURCE_POSTGRES_main"])
		}
	})

	t.Run("carries both when they name different keys", func(t *testing.T) {
		got := merged(map[string]string{"STRIPE_API_KEY": "sk_live"}, bindings, records)
		if got["STRIPE_API_KEY"] != "sk_live" || len(got) != 2 {
			t.Errorf("merged = %v, want the secret beside the binding and nothing else", got)
		}
		if handed := decodeBinding(t, got["OCEL_RESOURCE_POSTGRES_main"]); !proto.Equal(handed, published) {
			t.Errorf("OCEL_RESOURCE_POSTGRES_main = %q, want the published binding", got["OCEL_RESOURCE_POSTGRES_main"])
		}
	})
}

func publishedRecord(t *testing.T, binding *bindingsv1.Binding, version int64) values.Published {
	t.Helper()
	encoded, err := protojson.Marshal(binding)
	if err != nil {
		t.Fatalf("render the binding: %v", err)
	}
	return values.Published{Name: binding.GetName(), Value: encoded, Version: version}
}

func TestEnv(t *testing.T) {
	t.Run("declares the live keys the child must ask for", func(t *testing.T) {
		l := live.New(fixedSource{}, []string{"DB_PASSWORD"}, nil, nil)

		if got := l.Env(); !slices.Equal(got, []string{"OCEL_LIVE_KEYS=DB_PASSWORD"}) {
			t.Errorf("Env = %q, want the declaration alone", got)
		}
		if got := (*live.Values)(nil).Env(); got != nil {
			t.Errorf("Env for a function with no live values = %q, want nothing", got)
		}
	})

	t.Run("never carries a live plaintext", func(t *testing.T) {
		const dbPassword = "pg-plaintext-must-not-be-exported"
		const sessionSecret = "session-plaintext-must-not-be-exported"
		secrets := []string{dbPassword, sessionSecret}

		l := live.New(fixedSource{
			"DB_PASSWORD":    dbPassword,
			"SESSION_SECRET": sessionSecret,
		}, []string{"DB_PASSWORD", "SESSION_SECRET"}, nil, nil)
		if err := l.Join(l.Prefetch(context.Background())); err != nil {
			t.Fatalf("prefetch: %v", err)
		}

		before := os.Environ()
		got := l.Env()

		for _, entry := range got {
			for _, secret := range secrets {
				if strings.Contains(entry, secret) {
					t.Errorf("Env put %q in the child's environment, which discloses a live plaintext to anything that reads that environment", entry)
				}
			}
		}
		if want := []string{"OCEL_LIVE_KEYS=DB_PASSWORD,SESSION_SECRET"}; !slices.Equal(got, want) {
			t.Errorf("Env = %q, want exactly %q", got, want)
		}

		for _, entry := range os.Environ() {
			for _, secret := range secrets {
				if strings.Contains(entry, secret) {
					t.Errorf("Env set %q on this process's own environment", entry)
				}
			}
		}
		if !slices.Equal(os.Environ(), before) {
			t.Errorf("Env changed this process's environment; it may only compose a slice")
		}
	})
}

func TestGrantLag(t *testing.T) {
	bound := func(granted int64) live.Binding {
		binding := postgresBinding()
		binding.Granted = granted
		return binding
	}
	published := func(version int64) []values.Published {
		return []values.Published{{Name: "main", Version: version}}
	}

	t.Run("names the publishes an app's grants are behind", func(t *testing.T) {
		got := grantLag([]live.Binding{bound(3)}, published(5))
		if len(got) != 1 {
			t.Fatalf("grantLag = %v, want the lag reported once", got)
		}
		for _, want := range []string{"main", "2 more time", "version 3", "version 5"} {
			if !strings.Contains(got[0].Message, want) {
				t.Errorf("report = %q, want it to carry %q", got[0].Message, want)
			}
		}
	})

	t.Run("says nothing while the grants match the record", func(t *testing.T) {
		if got := grantLag([]live.Binding{bound(5)}, published(5)); len(got) != 0 {
			t.Errorf("grantLag = %v, want silence when the running grants came from the live version", got)
		}
	})

	t.Run("says nothing for a binding this deploy provisioned and granted in one pass", func(t *testing.T) {
		if got := grantLag([]live.Binding{postgresBinding()}, published(9)); len(got) != 0 {
			t.Errorf("grantLag = %v, want no lag where publish and grant are the same act", got)
		}
	})

	t.Run("repeats itself only when the record moves again", func(t *testing.T) {
		source := &storeSource{bindings: []live.Binding{bound(3)}}

		if got := source.unreportedGrantLag(published(5)); len(got) != 1 {
			t.Fatalf("first refresh reported %v, want the lag once", got)
		}
		if got := source.unreportedGrantLag(published(5)); len(got) != 0 {
			t.Errorf("second refresh reported %v, want a standing lag reported once, not on every refresh", got)
		}
		if got := source.unreportedGrantLag(published(6)); len(got) != 1 {
			t.Errorf("a further publish reported %v, want the widened lag named again", got)
		}
	})
}
