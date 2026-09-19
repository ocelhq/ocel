package devstack_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker/dockertest"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func postgres(name string) declare.Resource {
	return declare.Resource{Name: name, Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Postgres: &resourcesv1.PostgresConfig{Version: "17"}}
}

func TestEveryBindableResourceTypeHasAComponent(t *testing.T) {
	t.Parallel()

	for number := range resourcesv1.ResourceType_name {
		kind := resourcesv1.ResourceType(number)
		if _, bindable := naming.BindableAs(kind); !bindable {
			continue
		}
		if !devstack.Serves(kind) {
			t.Errorf("%s can be declared and no component serves it in dev", kind)
		}
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	t.Run("an app that declares nothing never reaches for docker", func(t *testing.T) {
		t.Parallel()

		opened := false
		stack := devstack.New("shop", devstack.Env{Open: func(context.Context) (docker.Engine, error) {
			opened = true
			return nil, errors.New("no daemon")
		}})

		resolved, err := stack.Resolve(context.Background(), nil)
		if err != nil || len(resolved) != 0 {
			t.Fatalf("Resolve = %v, %v, want nothing and no error", resolved, err)
		}
		if opened {
			t.Fatal("docker was opened for an app that declares no resource")
		}
	})

	t.Run("dispatches each declaration to its kind and says where it landed, once", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		var out bytes.Buffer
		stack := devstack.New("shop", devstack.Env{Open: engine.Opener(), Stdout: &out})

		for range 2 {
			resolved, err := stack.Resolve(context.Background(), []declare.Resource{postgres("main")})
			if err != nil {
				t.Fatalf("Resolve = %v", err)
			}
			if len(resolved) != 1 || resolved[0].Env["OCEL_RESOURCE_POSTGRES_main"] == "" {
				t.Fatalf("resolved = %+v, want main's binding", resolved)
			}
		}

		if got, want := out.String(), "postgres \"main\" → postgres:17 @ 127.0.0.1:54001\n"; got != want {
			t.Fatalf("printed %q, want %q exactly once", got, want)
		}
	})

	t.Run("a missing daemon is refused by naming the resource that needed it", func(t *testing.T) {
		t.Parallel()

		stack := devstack.New("shop", devstack.Env{Open: func(context.Context) (docker.Engine, error) {
			return nil, &docker.Unreachable{Address: "unix:///var/run/docker.sock", Err: errors.New("connect: no such file or directory")}
		}})

		_, err := stack.Resolve(context.Background(), []declare.Resource{postgres("main")})
		if err == nil {
			t.Fatal("Resolve = nil with no docker daemon")
		}
		for _, want := range []string{`postgres "main"`, "unix:///var/run/docker.sock", "start docker", "DOCKER_HOST"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), want)
			}
		}
	})

	t.Run("close stops what was started and lets go of the daemon", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{}
		stack := devstack.New("shop", devstack.Env{Open: engine.Opener()})
		if _, err := stack.Resolve(context.Background(), []declare.Resource{postgres("main")}); err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if err := stack.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v", err)
		}
		if len(engine.Stopped) != 1 || !engine.Closed {
			t.Fatalf("stopped %v, closed %v, want the one container stopped and the client closed", engine.Stopped, engine.Closed)
		}
	})
}

func TestResetWipesOnlyThisProjectsVolumes(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	if err := devstack.Reset(context.Background(), engine.Opener(), "shop-1a2b"); err != nil {
		t.Fatalf("Reset = %v", err)
	}
	if len(engine.Wiped) != 1 || engine.Wiped[0]["dev.ocel.project"] != "shop-1a2b" || len(engine.Wiped[0]) != 1 {
		t.Fatalf("wiped %v, want exactly the volumes labelled with this project", engine.Wiped)
	}
}

func TestProjectNamesAreReadableAndDistinctPerDirectory(t *testing.T) {
	t.Parallel()

	if got := devstack.ProjectName("/work/trees/sdk-node/My Shop"); got != "my-shop-2d2cdb07" {
		t.Fatalf("ProjectName = %q, want my-shop-2d2cdb07, the name the journey harness looks containers up by", got)
	}
	a, b := devstack.ProjectName("/home/ada/work/My Shop"), devstack.ProjectName("/home/ada/play/My Shop")
	if !strings.HasPrefix(a, "my-shop-") || a == b {
		t.Fatalf("ProjectName = %q and %q, want a readable name that differs between two directories called the same", a, b)
	}
}
