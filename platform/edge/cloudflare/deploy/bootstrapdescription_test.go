package cloudflare

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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

func partNamed(parts []edge.BootstrapPart, name string) (edge.BootstrapPart, bool) {
	for _, part := range parts {
		if part.Name == name {
			return part, true
		}
	}
	return edge.BootstrapPart{}, false
}

func TestTheCloudflareEdgeDescribesItsWorkerClientCertificate(t *testing.T) {
	m := bootstrapMock(t)
	p := mutualTLSEdge(t, m)
	names := []string{productionCertificateBase}

	fresh, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if part, found := partNamed(fresh, name); !found || part.Current {
			t.Errorf("fresh part %q = %+v, found %v, want it listed and not current", name, part, found)
		}
	}

	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	installed, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if part, found := partNamed(installed, name); !found || !part.Current {
			t.Errorf("installed part %q = %+v, found %v, want it current", name, part, found)
		}
	}
}

func TestACertificateDueForRenewalShowsTheCertificateNotCurrent(t *testing.T) {
	m := bootstrapMock(t)
	p := mutualTLSEdge(t, m)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	m.mtlsCertificates[0]["expires_on"] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)

	parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}

	if part, _ := partNamed(parts, productionCertificateBase); part.Current {
		t.Errorf("part %q is current, want it stale: the renewal uploads a new certificate", productionCertificateBase)
	}
	for _, name := range []string{sharedStoreScriptName} {
		if part, _ := partNamed(parts, name); !part.Current {
			t.Errorf("part %q is stale, want it unaffected by the renewal", name)
		}
	}
}

func TestACertificatesBootstrapPartKeepsItsNameAcrossARenewal(t *testing.T) {
	m := bootstrapMock(t)
	p := mutualTLSEdge(t, m)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	before, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	m.mtlsCertificates[0]["expires_on"] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	after, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}

	certificates := func(parts []edge.BootstrapPart) []string {
		var named []string
		for _, part := range parts {
			if strings.HasPrefix(part.Name, productionCertificateBase) {
				named = append(named, part.Name)
			}
		}
		return named
	}
	if got, want := certificates(after), certificates(before); !reflect.DeepEqual(got, want) || len(want) != 1 || want[0] != productionCertificateBase {
		t.Errorf("certificate parts = %v after a renewal and %v before, want the one stable name %q: a status entry must not churn with the dated upload", got, want, productionCertificateBase)
	}
}
