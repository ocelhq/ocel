package postgres_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
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
		component := postgres.New(engine.Opener(), t.TempDir())

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
			t.Errorf("binding = %s:%d/%s as %q with a password set %t, want the container's address and the database named for the resource", props.GetHost(), props.GetPort(), props.GetDatabase(), props.GetUsername(), props.GetPassword() != "")
		}
	})

	t.Run("two major versions get a container each", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		if _, err := postgres.New(engine.Opener(), t.TempDir()).Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17"), declared("legacy", "15")}); err != nil {
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
		component := postgres.New(engine.Opener(), t.TempDir())
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
		_, err := postgres.New(engine.Opener(), t.TempDir()).Resolve(context.Background(), "shop", []declare.Resource{declared("main", "9")})
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
		component := postgres.New(engine.Opener(), t.TempDir())
		if _, err := component.Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17"), declared("legacy", "15")}); err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if err := component.Close(context.Background(), true); err != nil {
			t.Fatalf("Close = %v", err)
		}
		if len(engine.Stopped) != 2 {
			t.Fatalf("stopped %v, want both containers", engine.Stopped)
		}
	})
}

func TestAnInterruptWhilePostgresComesUpLeavesAContainerCloseStillStops(t *testing.T) {
	t.Parallel()

	ctx, interrupt := context.WithCancel(context.Background())
	engine := &dockertest.Engine{}
	engine.Answer = func(argv []string) (string, error) {
		if argv[0] == "pg_isready" {
			interrupt()
			return "", errors.New("no response")
		}
		return "", nil
	}
	component := postgres.New(engine.Opener(), t.TempDir())

	if _, err := component.Resolve(ctx, "shop", []declare.Resource{declared("main", "17")}); err == nil {
		t.Fatal("Resolve = nil for a startup that was interrupted")
	}
	if err := component.Close(context.Background(), true); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if len(engine.Stopped) != 1 {
		t.Fatalf("stopped %v, want the container that was running when the interrupt landed", engine.Stopped)
	}
}

func TestAPostgresThatWasNotReadyIsPreparedAgainOnTheNextSync(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	failing := true
	engine.Answer = func(argv []string) (string, error) {
		if argv[0] == "psql" && failing && engine.Inputs[len(engine.Inputs)-1] != "" {
			return "", &docker.ExecFailed{Argv: argv, Code: 2, Output: "the server is starting up"}
		}
		return "", nil
	}
	component := postgres.New(engine.Opener(), t.TempDir())

	if _, err := component.Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17")}); err == nil {
		t.Fatal("Resolve = nil though the password could not be set")
	}
	failing = false
	if _, err := component.Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17")}); err != nil {
		t.Fatalf("second Resolve = %v", err)
	}
	if len(engine.Specs) != 1 {
		t.Fatalf("ran %d containers, want the one from the first sync reused", len(engine.Specs))
	}
	set := 0
	for _, input := range engine.Inputs {
		if strings.Contains(input, "PASSWORD") {
			set++
		}
	}
	if set != 2 {
		t.Fatalf("the password was set %d times, want it set again once the server answered", set)
	}
}

func TestThePasswordNeverReachesArgvOrAnError(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	engine := &dockertest.Engine{}
	engine.Answer = func(argv []string) (string, error) {
		if input := engine.Inputs[len(engine.Inputs)-1]; input != "" {
			return "", &docker.ExecFailed{Argv: argv, Code: 3, Output: "ERROR:  syntax error\nLINE 1: " + input}
		}
		return "", nil
	}

	_, err := postgres.New(engine.Opener(), state).Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17")})
	if err == nil {
		t.Fatal("Resolve = nil though the statement failed")
	}

	password := keptPassword(t, state)
	if strings.Contains(err.Error(), password) {
		t.Fatalf("the error repeats the password: %v", err)
	}
	for _, argv := range engine.Execs {
		if strings.Contains(dockertest.Joined(argv), password) {
			t.Fatalf("the password rode in argv: %v", argv)
		}
	}
}

func keptPassword(t *testing.T, state string) string {
	t.Helper()
	kept, err := filepath.Glob(filepath.Join(state, "*"))
	if err != nil || len(kept) != 1 {
		t.Fatalf("state holds %v (%v), want the one password file", kept, err)
	}
	info, err := os.Stat(kept[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the password file is %v, want it readable by its owner alone", info.Mode().Perm())
	}
	raw, err := os.ReadFile(kept[0])
	if err != nil || len(raw) == 0 {
		t.Fatalf("read the password file: %q, %v", raw, err)
	}
	return string(raw)
}

func TestTwoProcessesOfOneProjectHandOutTheSamePassword(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	var passwords []string
	for range 2 {
		engine := &dockertest.Engine{}
		resolved, err := postgres.New(engine.Opener(), state).Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17")})
		if err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		passwords = append(passwords, binding(t, resolved[0].Env["OCEL_RESOURCE_POSTGRES_main"]).GetPostgres().GetPassword())
	}
	if passwords[0] != passwords[1] || passwords[0] != keptPassword(t, state) {
		t.Fatalf("passwords = %v, want both the one kept for the project", passwords)
	}
}

func TestCloseLeavesContainersAnotherProcessStillUses(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	component := postgres.New(engine.Opener(), t.TempDir())
	if _, err := component.Resolve(context.Background(), "shop", []declare.Resource{declared("main", "17")}); err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if err := component.Close(context.Background(), false); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if len(engine.Stopped) != 0 {
		t.Fatalf("stopped %v, want the container left running", engine.Stopped)
	}
}
