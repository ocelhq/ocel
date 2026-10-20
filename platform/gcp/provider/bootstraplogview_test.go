package gcp

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/api/logging/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func logViewOf(tier environment.Tier) bootstrap.Resource {
	return bootstrap.Resource{Kind: bootstrap.KindLogView, Name: googletest.Names.LogView(tier)}
}

func TestTheBaseGivesEachTierALogViewOfItsOwnCloudRunEntries(t *testing.T) {
	t.Parallel()

	for _, tier := range googletest.BothTiers {
		view := logViewOf(tier)
		at := slices.IndexFunc(bootstrap.Resources(googletest.Names, tier, false), func(each bootstrap.Resource) bool { return each.ID() == view.ID() })
		if at < 0 {
			t.Fatalf("the %s base provisions %v, want %s: a deploy credential reads its tier's logs through it alone", tier, googletest.IdsOf(bootstrap.Resources(googletest.Names, tier, false)), view.ID())
		}
		if bootstrap.Resources(googletest.Names, tier, false)[at].Shared {
			t.Errorf("the %s log view is shared with the other tier, and it holds this tier's entries alone", tier)
		}
	}
	if want := `resource.type="cloud_run_revision" AND labels."ocel-tier"="preview"`; bootstrap.TierLogFilter(environment.TierPreview) != want {
		t.Errorf("the preview log view filters %q, want %q", bootstrap.TierLogFilter(environment.TierPreview), want)
	}
}

func TestABootstrapMakesItsTiersLogViewOnTheDefaultBucket(t *testing.T) {
	t.Parallel()
	views := googletest.NewLogViews()
	b := bootstrap.Engine{Clients: views.Open(t)}
	ctx := context.Background()
	tier := environment.TierPreview
	read := bootstrap.Survey{Tier: tier, Names: b.Clients.Names}
	view := logViewOf(tier)

	if found, err := b.PresenceOf(ctx, read, view); err != nil || found.Present {
		t.Fatalf("PresenceOf(an absent view) = %+v, %v, want it absent", found, err)
	}
	if err := view.KindOps().Make(ctx, b, read, view); err != nil {
		t.Fatalf("Make(log view) = %v", err)
	}
	made := views.Views[googletest.Names.LogViewPath(tier)]
	if made == nil || made.Filter != bootstrap.TierLogFilter(tier) {
		t.Fatalf("the bootstrap made %+v, want %s filtered to the preview tier's Cloud Run entries", views.Views, googletest.Names.LogViewPath(tier))
	}
	if found, err := b.PresenceOf(ctx, read, view); err != nil || !found.Present || found.Mends != "" {
		t.Errorf("PresenceOf(the view it made) = %+v, %v, want it present and current", found, err)
	}
	if err := view.KindOps().Make(ctx, b, read, view); err != nil {
		t.Errorf("Make(a view already made) = %v, want it kept", err)
	}
}

func TestALogViewFilteredToOtherEntriesIsMended(t *testing.T) {
	t.Parallel()
	views := googletest.NewLogViews()
	path := googletest.Names.LogViewPath(environment.TierPreview)
	views.Views[path] = &logging.LogView{Name: path, Filter: `resource.type="cloud_run_revision"`}
	b := bootstrap.Engine{Clients: views.Open(t)}
	ctx := context.Background()
	read := bootstrap.Survey{Tier: environment.TierPreview, Names: b.Clients.Names}
	view := logViewOf(environment.TierPreview)

	found, err := b.PresenceOf(ctx, read, view)
	if err != nil || !found.Present || found.Mends == "" {
		t.Fatalf("PresenceOf(a view of every tier's entries) = %+v, %v, want it present and mended: it lets the preview deploy credential read production's logs", found, err)
	}
	read.Held = map[string]bool{view.ID(): true}
	read.Mending = map[string]string{view.ID(): found.Mends}
	if err := b.Provision(ctx, read, view, nil); err != nil {
		t.Fatalf("Provision(a drifted view) = %v", err)
	}
	if got := views.Views[path].Filter; got != bootstrap.TierLogFilter(environment.TierPreview) {
		t.Errorf("the mended view filters %q, want %q", got, bootstrap.TierLogFilter(environment.TierPreview))
	}
}

func TestRemovingATierDeletesItsLogView(t *testing.T) {
	t.Parallel()
	views := googletest.NewLogViews()
	b := bootstrap.Engine{Clients: views.Open(t)}
	ctx := context.Background()
	tier := environment.TierProduction
	view := logViewOf(tier)
	read := bootstrap.Survey{Tier: tier, Names: b.Clients.Names, Sibling: true, Held: map[string]bool{view.ID(): true}}
	if err := view.KindOps().Make(ctx, b, read, view); err != nil {
		t.Fatal(err)
	}

	taking := bootstrap.Removing(read, view)
	if taking.Action != provider.ActionDelete {
		t.Fatalf("removing the %s tier plans its log view %s (%s), want it deleted while the other tier is installed: the view is this tier's own", tier, taking.Action, taking.Reason)
	}
	if _, err := b.RemoveResource(ctx, read, taking); err != nil {
		t.Fatalf("RemoveResource(log view) = %v", err)
	}
	if !slices.Equal(views.Deleted, []string{names.New("ocel", "acme-prod").LogViewPath(tier)}) {
		t.Errorf("removing the tier deleted %v, want its log view", views.Deleted)
	}
	if _, err := view.KindOps().Take(ctx, b, read, view); err != nil {
		t.Errorf("Take(a view already gone) = %v, want nothing to do", err)
	}
}

func TestABootstrapCredentialMayMakeAndRemoveATiersLogView(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{"logging.views.create", "logging.views.get", "logging.views.update", "logging.views.delete"} {
		if !slices.Contains(bootstrap.BootstrapPermissions, permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: the base makes, mends and removes its tier's log view", bootstrap.BootstrapPermissions, permission)
		}
	}
	if !slices.Contains(bootstrap.BootstrapRoles(), "roles/logging.configWriter") {
		t.Errorf("a bootstrap credential is granted %v, want roles/logging.configWriter, which holds the log view permissions", bootstrap.BootstrapRoles())
	}
}
