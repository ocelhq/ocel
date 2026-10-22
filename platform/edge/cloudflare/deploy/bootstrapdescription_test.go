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
		sharedStoreScriptName,
		sharedStoreScriptName + "/" + bootstrapSecretBinding,
	}

	t.Run("a fresh account has no part current", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)

		parts, err := m.provider(t).Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("DescribeBootstrap: %v", err)
		}
		if want := describedParts(false, names...); !reflect.DeepEqual(parts, want) {
			t.Errorf("parts = %+v, want %+v", parts, want)
		}
	})

	t.Run("an installed bootstrap has every part current", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
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

	t.Run("a drifted releases store leaves its secret current", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		releasesStoreBundle = []byte("export default {rebuilt:1}")

		parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("DescribeBootstrap: %v", err)
		}
		want := describedParts(true, names...)
		want[0].Current = false
		if !reflect.DeepEqual(parts, want) {
			t.Errorf("parts = %+v, want %+v", parts, want)
		}
	})
}

func TestDescribingTheBootstrapWithTheChecksSkippedReadsNothingAndCallsItCurrent(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)
	p := m.provider(t)
	p.skipChecks = true

	parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("DescribeBootstrap: %v", err)
	}
	if m.requests != 0 {
		t.Errorf("DescribeBootstrap with the checks skipped sent Cloudflare %d requests, want none", m.requests)
	}
	if len(parts) == 0 || slices.ContainsFunc(parts, func(part edge.BootstrapPart) bool { return !part.Current }) {
		t.Errorf("parts = %+v, want the bootstrap described and current", parts)
	}
}

func TestATokenWithoutAPITokensWriteOrR2PlansInstallsDescribesAndTearsTheBootstrapDown(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)
	p := m.provider(t)

	if _, err := p.planBootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("planBootstrap: %v", err)
	}
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("DescribeBootstrap: %v", err)
	}
	for _, part := range parts {
		if !part.Current {
			t.Errorf("part %q is described as behind, want every installed part current: %+v", part.Name, parts)
		}
	}
	if _, err := p.planRemoveBootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("planRemoveBootstrap: %v", err)
	}
	if err := p.Teardown(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	assertNoAPITokenOrR2Calls(t, m)
}

func TestDescribingACloudflareBootstrapWithoutCredentialsIsAnError(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)
	t.Setenv(envAPIToken, "")

	_, err := m.provider(t).Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err == nil || !strings.Contains(err.Error(), envAPIToken) {
		t.Fatalf("DescribeBootstrap error = %v, want one naming %s", err, envAPIToken)
	}
}
