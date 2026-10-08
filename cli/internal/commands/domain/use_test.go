package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func previewProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview, fake.FeatureCache, fake.FeatureImages)
	return project
}

func writeZonedPreviewConfig(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: { dns: "zone" } },
  domains: { preview: "*.preview.acme.com" },
};
`)
}

func useWildcard(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	invocation := newTestInvocation()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	if err := runDomainUse(context.Background(), invocation, project.Root, "*.preview.acme.com", domainOptions{preview: true}, &stdout); err != nil {
		t.Fatalf("use the global preview domain: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func servedOnWildcard(t *testing.T, project clitest.FakeProject, slug string, previews ...string) {
	t.Helper()
	ctx := context.Background()
	store := project.Provider.KeyValues()
	name := stackrecords.EdgeStackKey(environment.TierPreview, slug)
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		t.Fatal(err)
	}
	state := stackrecords.EdgeState{Edge: edge.StackState{Slug: slug, Tier: environment.TierPreview, GlobalPreview: "preview.acme.com"}}
	if recorded.Value, err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, recorded); err != nil {
		t.Fatal(err)
	}
	for _, preview := range previews {
		stack := stackrecords.StackKey(environment.TierPreview, slug, naming.AppStack(preview, "web", naming.NewReleaseToken("b1", "")))
		entry, err := keyvalue.ReadOrEmpty(ctx, store, stack)
		if err != nil {
			t.Fatal(err)
		}
		entry.Value = []byte("{}")
		if _, err := store.Write(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
}

func relayEdge(project clitest.FakeProject) *fake.Edge {
	return project.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay)
}

func TestDomainUseServesEveryProjectsPreviewsOnTheWildcard(t *testing.T) {
	t.Run("use claims the wildcard's base domain", func(t *testing.T) {
		project := previewProject(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainUse(context.Background(), invocation, project.Root, "*.preview.acme.com", domainOptions{preview: true}, &stdout); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if out := stdout.String(); !strings.Contains(out, "Previews are served on *.preview.acme.com") {
			t.Errorf("stdout = %q, want it to say previews are served on the wildcard", out)
		}
		asked := clitest.RequestsTo[*contractv1.UsePreviewWildcardRequest](t, project.Requests, contractv1connect.ProviderServiceUsePreviewWildcardProcedure)
		if len(asked) != 1 || asked[0].GetBaseDomain() != "preview.acme.com" || asked[0].GetTier() != environmentv1.Tier_TIER_PREVIEW {
			t.Errorf("the CLI asked %v, want the preview tier to use the base domain preview.acme.com", asked)
		}
		if raised := relayEdge(project).Wildcard(); raised != "preview.acme.com" {
			t.Errorf("the edge serves the wildcard of %q, want preview.acme.com", raised)
		}
	})

	t.Run("use without a dns prints the record to add", func(t *testing.T) {
		project := previewProject(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainUse(context.Background(), invocation, project.Root, "*.preview.acme.com", domainOptions{preview: true}, &stdout); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"Point *.preview.acme.com at the relay edge — add this record at your DNS provider", "CNAME  *.preview.acme.com  preview.relay.fake.invalid"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "Writing") {
			t.Errorf("stdout = %q, want no record written without a dns", out)
		}
	})

	t.Run("use with a dns writes the record", func(t *testing.T) {
		project := previewProject(t)
		writeZonedPreviewConfig(t, project.Root)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainUse(context.Background(), invocation, project.Root, "*.preview.acme.com", domainOptions{preview: true}, &stdout); err != nil {
			t.Fatalf("runDomainUse err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if out := stdout.String(); !strings.Contains(out, "Writing *.preview.acme.com CNAME preview.relay.fake.invalid") {
			t.Errorf("stdout = %q, want it to say it writes the wildcard's record", out)
		}
		written := project.Provider.DNS().(*fake.DNS).Zone("").Records()
		if !slices.ContainsFunc(written, func(record edge.Record) bool { return record.Name == "*.preview.acme.com" }) {
			t.Errorf("the zone holds %v, want the wildcard's record", written)
		}
	})

	t.Run("use refuses an argument that is not a leading wildcard", func(t *testing.T) {
		project := previewProject(t)
		invocation := newTestInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainUse(context.Background(), invocation, project.Root, "preview.acme.com", domainOptions{preview: true}, &stdout)
		if err == nil {
			t.Fatal("runDomainUse err = nil, want a wildcard refusal")
		}
		if !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("err = %v, want it to name the wildcard requirement", err)
		}
	})
}
