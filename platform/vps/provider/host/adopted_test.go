package host

import (
	"context"
	"slices"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type yours struct {
	handed *proxy.Spec
}

func (y yours) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (y yours) Render(spec proxy.Spec) ([]byte, error) {
	if y.handed != nil {
		*y.handed = spec
	}
	return []byte("routes " + spec.PreviewBase), nil
}

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
