package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestTheCommandThatEmptiesATierDestroysItsProjectsOnThatTierAlone(t *testing.T) {
	t.Parallel()

	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "ocel projects destroy --tier production",
		environment.TierPreview:    "ocel projects destroy --tier preview",
	} {
		if got := destroyIn(tier); got != want {
			t.Errorf("destroyIn(%s) = %q, want %q", tier, got, want)
		}
	}
}

func describing(t *testing.T, skipChecks bool) (Engine, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (&googletest.ServiceUsage{}).Serve(w, r) {
			return
		}
		calls.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(server.Close)
	clients := &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL, Records: fake.NewKeyValues()}
	return Engine{ServiceStates: &ServiceStates{}, Clients: clients, Surveyed: &Surveys{}, SkipChecks: skipChecks}, &calls
}

func TestABootstrapStatusReadsTheBootstrapsResourcesOnlyWhenTheChecksAreNotSkipped(t *testing.T) {
	t.Parallel()

	for skipChecks, wantReads := range map[bool]bool{false: true, true: false} {
		b, calls := describing(t, skipChecks)

		if _, err := b.Describe(context.Background(), environment.TierProduction); err != nil {
			t.Fatalf("Describe() with the checks skipped %t = %v", skipChecks, err)
		}
		if read := calls.Load() > 0; read != wantReads {
			t.Errorf("Describe() with the checks skipped %t made %d calls, want resources read %t: the harness that skips them ran the drift check once before its deploys", skipChecks, calls.Load(), wantReads)
		}
		if _, remembered := b.Surveyed.recall(environment.TierProduction); remembered == skipChecks {
			t.Errorf("Describe() with the checks skipped %t remembered its survey %t, want %t: a bootstrap that recalls a survey that read no resource provisions every one of them again", skipChecks, remembered, !skipChecks)
		}
	}
}

func TestABootstrapWhoseResourcesWereNotReadIsCurrentWhenItsStampNamesWhatThisBuildProvisions(t *testing.T) {
	t.Parallel()

	b := Engine{ServiceStates: &ServiceStates{}, Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}}
	stamped := func(digest string, resourcesRead bool) Survey {
		read := b.newSurvey(environment.TierProduction)
		read.Present, read.ResourcesRead = true, resourcesRead
		read.Stamp = Stamp{State: StateComplete, Digest: digest}
		return read
	}
	current := DigestOf(b.Clients.Namespace(), Resources(b.Clients.Names, environment.TierProduction, false))

	for name, tc := range map[string]struct {
		read Survey
		want bool
	}{
		"unread, stamped by this build":       {read: stamped(current, false), want: true},
		"unread, stamped by another build":    {read: stamped("an earlier digest", false), want: false},
		"read, holding none of its resources": {read: stamped(current, true), want: false},
	} {
		described, err := b.Described(context.Background(), tc.read)
		if err != nil {
			t.Fatalf("%s: Described() = %v", name, err)
		}
		if got := described.Stacks[0].DigestCurrent; got != tc.want {
			t.Errorf("%s: the bootstrap is current %t, want %t", name, got, tc.want)
		}
	}
}
