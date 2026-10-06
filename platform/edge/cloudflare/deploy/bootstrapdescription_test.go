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

func partNamed(parts []edge.BootstrapPart, name string) (edge.BootstrapPart, bool) {
	for _, part := range parts {
		if part.Name == name {
			return part, true
		}
	}
	return edge.BootstrapPart{}, false
}

func TestTheCloudflareEdgeDescribesItsWorkerClientCertificateRefreshQueueAndRefresher(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	names := []string{productionCertificateBase, refreshQueueName, refresherScript, refreshQueueName + "/" + refresherScript}

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

func TestACertificateDueForRenewalShowsTheCertificateAndTheRefresherNotCurrent(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	m.mtlsCertificates[0]["expires_on"] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)

	parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{productionCertificateBase, refresherScript} {
		if part, _ := partNamed(parts, name); part.Current {
			t.Errorf("part %q is current, want it stale: the renewal uploads a new certificate and rebinds the refresher", name)
		}
	}
	for _, name := range []string{refreshQueueName, sharedStoreScriptName} {
		if part, _ := partNamed(parts, name); !part.Current {
			t.Errorf("part %q is stale, want it unaffected by the renewal", name)
		}
	}
}

func TestACertificatesBootstrapPartKeepsItsNameAcrossARenewal(t *testing.T) {
	m := bootstrapMock(t, false)
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

func TestAQueueDrainedByAnotherConsumerShowsItsConsumerNotCurrent(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	consumers := m.queues[0]["consumers"].([]any)
	m.queues[0]["consumers"] = append(consumers, map[string]any{"consumer_id": "foreign", "script": "someone-elses", "type": "worker", "settings": map[string]any{}})

	parts, err := p.Hooks().DescribeBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if part, _ := partNamed(parts, refreshQueueName+"/"+refresherScript); part.Current {
		t.Error("the consumer part is current while another consumer drains the queue")
	}
}
