package cloudflare

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func describedParts(current bool, names ...string) []edge.BootstrapPart {
	parts := make([]edge.BootstrapPart, 0, len(names))
	for _, name := range names {
		parts = append(parts, edge.BootstrapPart{Name: name, Current: current})
	}
	return parts
}

func TestTheCloudflareEdgeDescribesEachBootstrapPartAndWhetherItIsCurrent(t *testing.T) {
	names := []string{
		cacheStoreName(environment.TierProduction),
		sharedStoreScriptName,
		sharedStoreScriptName + "/" + bootstrapSecretBinding,
		isrWriterScriptName,
		isrWriterScriptName + "/" + bootstrapSecretBinding,
	}

	t.Run("a fresh account has no part current", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, false)

		parts, err := m.provider(t).Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("DescribeBootstrap: %v", err)
		}
		if want := describedParts(false, names...); !reflect.DeepEqual(parts, want) {
			t.Errorf("parts = %+v, want %+v", parts, want)
		}
	})

	t.Run("an installed bootstrap has every part current", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}

		parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("DescribeBootstrap: %v", err)
		}
		if want := describedParts(true, names...); !reflect.DeepEqual(parts, want) {
			t.Errorf("parts = %+v, want %+v", parts, want)
		}
	})

	t.Run("a drifted isr-writer is the only part not current", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		isrWriterBundle = []byte("export default {writer:2}")

		parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("DescribeBootstrap: %v", err)
		}
		want := describedParts(true, names...)
		want[3].Current = false
		if !reflect.DeepEqual(parts, want) {
			t.Errorf("parts = %+v, want %+v", parts, want)
		}
	})
}

func TestDescribingACloudflareBootstrapWithoutCredentialsIsAnError(t *testing.T) {
	seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
	m := bootstrapMock(t, true)
	t.Setenv(envAPIToken, "")

	_, err := m.provider(t).Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err == nil || !strings.Contains(err.Error(), envAPIToken) {
		t.Fatalf("DescribeBootstrap error = %v, want one naming %s", err, envAPIToken)
	}
}

func TestTheBootstrapStatusListsTheRefreshQueueAndItsRefresher(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)

	parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("DescribeBootstrap: %v", err)
	}
	names := []string{refreshQueueName, refresherScript, refreshQueueName + "/" + refresherScript}
	for _, name := range names {
		if !slices.ContainsFunc(parts, func(part edge.BootstrapPart) bool { return part.Name == name && !part.Current }) {
			t.Errorf("parts = %+v, want %q listed and not current on a fresh account", parts, name)
		}
	}

	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	parts, err = p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !slices.ContainsFunc(parts, func(part edge.BootstrapPart) bool { return part.Name == name && part.Current }) {
			t.Errorf("parts = %+v, want %q current once bootstrapped", parts, name)
		}
	}
}
