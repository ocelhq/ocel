package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type yours struct {
	file   string
	handed *proxy.Spec
}

func (y yours) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (y yours) Render(spec proxy.Spec) ([]byte, error) {
	if y.handed != nil {
		*y.handed = spec
	}
	return []byte("routes " + spec.PreviewBase), nil
}

func (y yours) File() string { return y.file }

func (yours) Unrendered([]byte, proxy.Permission) string { return "" }

func (yours) Reload(context.Context) error { return nil }

func (yours) Inspect(context.Context) (proxy.Standing, error) { return nil, nil }

func (yours) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{}, nil
}

func TestAProxyIsHandedEveryHostnameTheBoxServesAndThePreviewBase(t *testing.T) {
	t.Parallel()

	state := previewing()
	state.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "pr-7." + previewBase, Owner: surface, Pointer: "@pr-7"},
	}
	state.Connector = "console.example.com"
	var handed proxy.Spec
	if _, err := RenderProxyConfig(yours{handed: &handed}, state); err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	want := []string{"console.example.com", edge.ProbeHostname(edge.PreviewWildcard(previewBase)), "pr-7." + previewBase, claimed}
	slices.Sort(want)
	if !slices.Equal(handed.Hostnames, want) {
		t.Errorf("the proxy is handed hostnames %q, want %q: a proxy that names what it routes routes nothing it is not told of, and the preview claim, the connector and the edge probe are each served here", handed.Hostnames, want)
	}
	if handed.PreviewBase != previewBase {
		t.Errorf("the proxy is handed preview base %q, want %q for a wildcard certificate over the previews", handed.PreviewBase, previewBase)
	}
}

const coolifyDynamic = "/data/coolify/proxy/dynamic"

func TestTheSwitchboardIsHandedTheDirectoryOfAFileOutsideOcelsOwnToPlaceInAndNoOther(t *testing.T) {
	t.Parallel()

	board := placing(switchboardOf(t, routedByHand()), coolifyDynamic+"/ocel.yml")
	running := board.run()
	if at := slices.Index(running, coolifyDynamic+":"+coolifyDynamic); at < 1 || running[at-1] != "--volume" {
		t.Errorf("the switchboard runs as %q, want %s bound into it read-write: ocel-deploy cannot write there, and the switchboard places what it renders", running, coolifyDynamic)
	}
	if at := slices.Index(running, switchboard.PlaceEnv+"="+coolifyDynamic); at < 1 || running[at-1] != "--env" {
		t.Errorf("the switchboard runs as %q, want %s set to %s: it refuses to place anywhere else", running, switchboard.PlaceEnv, coolifyDynamic)
	}
	if !strings.Contains(string(board.facts()), "bind="+coolifyDynamic+":"+coolifyDynamic+"\n") {
		t.Errorf("the switchboard's facts read\n%s\nand never name the directory it places in, so a bootstrap that moved it plans nothing", board.facts())
	}

	for what, front := range map[string]Front{"ocel's own proxy": {}, "a proxy routed by hand": routedByHand()} {
		for _, bound := range switchboardOf(t, front).binds {
			if !strings.HasSuffix(bound, ":ro") && !slices.Contains([]string{switchboard.ControlDir, switchboard.FrontDir}, strings.SplitN(bound, ":", 2)[0]) {
				t.Errorf("the switchboard beside %s is bound %s read-write, want no directory to place in: nothing it renders goes anywhere ocel-deploy cannot write", what, bound)
			}
		}
		if env := switchboardOf(t, front).env; slices.ContainsFunc(env, func(set string) bool { return strings.HasPrefix(set, switchboard.PlaceEnv+"=") }) {
			t.Errorf("the switchboard beside %s runs with %q, want no directory to place in", what, env)
		}
	}
}
