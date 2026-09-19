package postgres_test

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker/dockertest"
	"github.com/ocelhq/ocel/cli/internal/devstack/postgres"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func declared(name, version string) declare.Resource {
	return declare.Resource{
		Name:     name,
		Type:     resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
		Postgres: &resourcesv1.PostgresConfig{Version: version},
	}
}

func binding(t *testing.T, raw string) *bindingsv1.Binding {
	t.Helper()
	var b bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("the binding is not the JSON the SDKs read: %v\n%s", err, raw)
	}
	return &b
}

func TestResolve(t *testing.T) {
	t.Parallel()

	t.Run("one database per declared name, in one container per major version", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		component := postgres.New(engine.Opener())

		resolved, err := component.Resolve(context.Background(), "shop-1a2b", []declare.Resource{declared("main", "17"), declared("audit", "17")})
		if err != nil {
			t.Fatalf("Resolve = %v", err)
		}

		if len(engine.Specs) != 1 {
			t.Fatalf("ran %d containers, want one shared by both version-17 databases", len(engine.Specs))
		}
		spec := engine.Specs[0]
		if !strings.HasPrefix(spec.Image, "postgres:17.") || !strings.Contains(spec.Image, "@sha256:") {
			t.Errorf("Image = %q, want a postgres 17 tag pinned by digest", spec.Image)
		}
		if spec.Labels["dev.ocel.project"] != "shop-1a2b" || spec.Labels["dev.ocel.component"] != "postgres" {
			t.Errorf("Labels = %v, want the project and the component", spec.Labels)
		}
		if spec.Volume == "" || !strings.Contains(spec.Volume, "shop-1a2b") || !strings.HasSuffix(spec.Volume, "17") {
			t.Errorf("Volume = %q, want one named for the project and the major version", spec.Volume)
		}

		var created []string
		for _, argv := range engine.Ran("createdb") {
			created = append(created, argv[len(argv)-1])
		}
		if strings.Join(created, ",") != "main,audit" {
			t.Errorf("created databases %v, want main and audit", created)
		}

		if len(resolved) != 2 {
			t.Fatalf("resolved %d resources, want 2", len(resolved))
		}
		main := resolved[0]
		if main.Origin != "postgres:17 @ 127.0.0.1:54001" {
			t.Errorf("Origin = %q", main.Origin)
		}
		props := binding(t, main.Env["OCEL_RESOURCE_POSTGRES_main"]).GetPostgres()
		if props.GetHost() != "127.0.0.1" || props.GetPort() != 54001 || props.GetDatabase() != "main" || props.GetUsername() != "postgres" || props.GetPassword() == "" {
			t.Errorf("binding = %+v, want the container's address and the database named for the resource", props)
		}
	})

	t.Run("two major versions get a container each", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		if _, err := postgres.New(engine.Opener()).Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17"), declared("legacy", "15")}); err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if len(engine.Specs) != 2 {
			t.Fatalf("ran %d containers, want one per major version", len(engine.Specs))
		}
	})

	t.Run("a second sync reuses the running container and creates only what is new", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		engine.Answer = func(argv []string) (string, error) {
			if argv[0] == "psql" && len(engine.Specs) > 0 && strings.Contains(dockertest.Joined(argv), "pg_database") {
				return "postgres\nmain\n", nil
			}
			return "", nil
		}
		component := postgres.New(engine.Opener())
		first, err := component.Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17")})
		if err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		second, err := component.Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17")})
		if err != nil {
			t.Fatalf("second Resolve = %v", err)
		}
		if len(engine.Specs) != 1 {
			t.Fatalf("ran %d containers across two syncs, want 1", len(engine.Specs))
		}
		if len(engine.Ran("createdb")) != 0 {
			t.Errorf("createdb ran for a database that already exists: %v", engine.Ran("createdb"))
		}
		if first[0].Env["OCEL_RESOURCE_POSTGRES_main"] != second[0].Env["OCEL_RESOURCE_POSTGRES_main"] {
			t.Error("the binding changed between two syncs of the same declaration")
		}
	})

	t.Run("refuses a version outside the pinned table and names the ones in it", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		_, err := postgres.New(engine.Opener()).Resolve(context.Background(), "shop", []declare.Resource{declared("main", "9")})
		if err == nil || !strings.Contains(err.Error(), `"main"`) || !strings.Contains(err.Error(), "17") {
			t.Fatalf("Resolve = %v, want a refusal naming the resource and the versions dev can run", err)
		}
		if len(engine.Specs) != 0 {
			t.Error("a container was started for a version that is not in the table")
		}
	})

	t.Run("close stops every container it started", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		component := postgres.New(engine.Opener())
		if _, err := component.Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17"), declared("legacy", "15")}); err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if err := component.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v", err)
		}
		if len(engine.Stopped) != 2 {
			t.Fatalf("stopped %v, want both containers", engine.Stopped)
		}
	})
}
