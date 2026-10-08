package build

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
)

func proxiedVariables() map[string]AppVariables {
	return map[string]AppVariables{"web": {
		BindingProxyEnv: map[string]string{
			processenv.RuntimeAddressEnvVar: "http://127.0.0.1:41999",
			localrpc.SessionTokenEnvVar:     "proxy-session-token",
		},
	}}
}

func TestABuildReachesTheBindingProxyThroughTheEnvironmentItIsHanded(t *testing.T) {
	root := t.TempDir()
	writeBuildScript(t, root)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

	var got nodeBuildRequest
	builder := nodeOnly{node: requestOf(&got)}
	if err := builder.Build(context.Background(), cfg, proxiedVariables(), Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	env := got.Apps[0].Env
	if env[processenv.RuntimeAddressEnvVar] != "http://127.0.0.1:41999" || env[localrpc.SessionTokenEnvVar] != "proxy-session-token" {
		t.Errorf("web was built with %v, want the runtime address and session token of the binding proxy", env)
	}
}

func TestABuildNeverShowsTheBindingProxysSessionToken(t *testing.T) {
	root := t.TempDir()
	writeBuildScript(t, root)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

	var shown bytes.Buffer
	builder := nodeOnly{node: func(_ context.Context, _ string, _ []byte, log Log) error {
		_, err := log.Shared.Write([]byte("fetch failed with Authorization: Bearer proxy-session-token\n"))
		return err
	}}
	if err := builder.Build(context.Background(), cfg, proxiedVariables(), Log{Shared: &shown}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if strings.Contains(shown.String(), "proxy-session-token") {
		t.Errorf("the build showed %q, want the session token hidden", shown.String())
	}
}

func TestAnAppCannotDeclareTheNamesTheBindingProxyIsDeliveredUnder(t *testing.T) {
	for _, name := range []string{processenv.RuntimeAddressEnvVar, localrpc.SessionTokenEnvVar} {
		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		builder := nodeOnly{node: requestOf(&nodeBuildRequest{})}
		err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Env: map[string]string{name: "mine"}}}, Log{})

		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("Build with %s declared = %v, want it refused by name", name, err)
		}
	}
}
