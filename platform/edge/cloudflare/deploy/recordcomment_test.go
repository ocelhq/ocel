package cloudflare

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

func TestARecordTheDNSWriterWroteNamesTheOriginItWroteItFor(t *testing.T) {
	t.Parallel()

	if got, want := dnsWriter(testOrigin).comment(), "managed by ocel on aws:123456789012/us-east-1"; got != want {
		t.Errorf("the DNS writer's comment = %q, want %q", got, want)
	}
	for _, origin := range []OriginScope{testOrigin, otherOrigin, {Vendor: "vps", ID: "k3j9x0"}, {Vendor: "gcp", ID: "example.com:shop"}} {
		comment := dnsWriter(origin).comment()
		read, written := parseRecordOwner(comment)
		if !written || read != dnsWriter(origin) || read.isForwarding() {
			t.Errorf("parseRecordOwner(%q) = %+v, %v, want a record %s wrote, forwarded for no one", comment, read, written, origin)
		}
	}
}

func TestARecordForwardedForAProjectNamesTheProjectAndItsOrigin(t *testing.T) {
	t.Parallel()

	owner := mustForwardingOwner(defaultNamespace, testOrigin, "acme", environment.TierProduction)
	tag := naming.HashTag(defaultNamespace, "acme", string(environment.TierProduction))
	if want := "ocel-prod-acme-" + tag + " on aws:123456789012/us-east-1"; owner.String() != want {
		t.Errorf("forwardingOwner = %q, want %q", owner, want)
	}
	if got, want := owner.comment(), "managed by ocel for ocel-prod-acme-"+tag+" on aws:123456789012/us-east-1"; got != want {
		t.Errorf("comment = %q, want %q", got, want)
	}
	for _, owner := range []recordOwner{owner, previewForwardingOwner(otherOrigin)} {
		read, written := parseRecordOwner(owner.comment())
		if !written || read != owner {
			t.Errorf("parseRecordOwner(%q) = %+v, %v, want it forwarded for %q", owner.comment(), read, written, owner)
		}
	}
	if read, _ := parseRecordOwner(previewForwardingOwner(otherOrigin).comment()); read.isOtherOrigin(otherOrigin) {
		t.Errorf("the preview wildcard %s forwards reads as %s's", otherOrigin, read.origin)
	}
}

func TestTheSameProjectForwardedOnTwoOriginsHasTwoOwners(t *testing.T) {
	t.Parallel()

	for _, tier := range workerTiers {
		if mustForwardingOwner(defaultNamespace, testOrigin, "acme", tier).comment() == mustForwardingOwner(defaultNamespace, otherOrigin, "acme", tier).comment() {
			t.Errorf("acme's %s forward on %s and on %s share one owner, so each would adopt and repoint the other's record", tier, testOrigin, otherOrigin)
		}
	}
	if previewForwardingOwner(testOrigin).comment() == previewForwardingOwner(otherOrigin).comment() {
		t.Errorf("the preview wildcard of %s and of %s share one owner, so each would overwrite the other's", testOrigin, otherOrigin)
	}
}

func TestThisOriginsPreviewWildcardIsOwnedByThePreviewEntryAndAnotherOriginsIsNot(t *testing.T) {
	t.Parallel()

	if got := domainOwner(previewForwardingOwner(testOrigin), testOrigin); got != edge.PreviewEntryOwner {
		t.Errorf("domainOwner of this origin's preview wildcard = %q, want %q", got, edge.PreviewEntryOwner)
	}
	theirs := previewForwardingOwner(otherOrigin)
	if got := domainOwner(theirs, testOrigin); got != theirs.String() {
		t.Errorf("domainOwner of %s's preview wildcard = %q, want %q", otherOrigin, got, theirs)
	}
}

func TestACommentThatNamesNoOriginWasNotWrittenByAnyOrigin(t *testing.T) {
	t.Parallel()

	for _, comment := range []string{
		"",
		"pointed here by hand",
		"managed by ocel",
		"managed by ocel for ocel--acme--production",
		"managed by ocel on ",
		"managed by ocel on aws",
		"managed by ocel on :123",
		"managed by ocel for  on aws:123",
		"managed by ocel for ocel--acme--production on aws:123 extra",
	} {
		if read, written := parseRecordOwner(comment); written {
			t.Errorf("parseRecordOwner(%q) = %+v, want no origin read from it", comment, read)
		}
	}
}

func mustForwardingOwner(namespace string, origin OriginScope, slug string, tier environment.Tier) recordOwner {
	owner, err := originNames{namespace: namespace, origin: origin}.forwardingOwner(slug, tier)
	if err != nil {
		panic(err)
	}
	return owner
}

const (
	longestNamespace = "a-namespace-far-longer-than-any-field-holds"
	longestSlug      = "a-project-slug-of-the-sixty-three-characters-a-slug-may-hold-xy"
)

var longestOrigins = []OriginScope{
	{Vendor: "aws", ID: "123456789012/ap-southeast-4"},
	{Vendor: "aws", ID: "123456789012/us-isob-east-1"},
	{Vendor: "gcp", ID: "a-thirty-character-project-id1"},
	{Vendor: "vps", ID: "k3j9x0"},
	{Vendor: "gcp", ID: strings.Repeat("x", maxOriginLength-len("gcp:"))},
}

func everyRecordComment(t *testing.T, namespaces, slugs []string, origins []OriginScope) map[string]string {
	t.Helper()
	comments := map[string]string{}
	add := func(comment, from string) {
		if held, taken := comments[comment]; taken {
			t.Errorf("%s and %s both write the comment %q, so each would take the other's record for its own", held, from, comment)
		}
		comments[comment] = from
	}
	for _, origin := range origins {
		add(dnsWriter(origin).comment(), "the DNS writer on "+origin.String())
		add(previewForwardingOwner(origin).comment(), "the preview wildcard on "+origin.String())
		for _, namespace := range namespaces {
			for _, slug := range slugs {
				for _, tier := range workerTiers {
					owner, err := originNames{namespace: namespace, origin: origin}.forwardingOwner(slug, tier)
					if err != nil {
						t.Fatalf("forwardingOwner(%q, %s, %q, %s): %v", namespace, origin, slug, tier, err)
					}
					add(owner.comment(), fmt.Sprintf("%s's %s forward in %q on %s", slug, tier, namespace, origin))
				}
			}
		}
	}
	return comments
}

func TestEveryRecordCommentFitsTheCommentAFreeZoneHolds(t *testing.T) {
	t.Parallel()

	if len(longestSlug) != 63 || len(naming.NamespaceField(longestNamespace)) != naming.MaxNamespaceField {
		t.Fatalf("the worst case is a 63-character slug in a namespace whose field is %d characters", naming.MaxNamespaceField)
	}
	for comment := range everyRecordComment(t, []string{defaultNamespace, longestNamespace}, []string{"a", longestSlug}, longestOrigins) {
		if len(comment) > maxRecordCommentLength {
			t.Errorf("%q is %d characters, want at most %d: a Free zone refuses a longer comment", comment, len(comment), maxRecordCommentLength)
		}
	}
}

func TestTwoOwnersNeverWriteTheSameComment(t *testing.T) {
	t.Parallel()

	slugs := []string{"a", "b", "acme", longestSlug, longestSlug[:62] + "z", longestSlug[:40], longestSlug[:40] + "-x"}
	namespaces := []string{defaultNamespace, longestNamespace, longestNamespace + "-2", "ocel-ci"}
	origins := append([]OriginScope{testOrigin, otherOrigin}, longestOrigins...)
	everyRecordComment(t, namespaces, slugs, origins)
}

func TestAnOriginTooLongForARecordCommentIsRefused(t *testing.T) {
	t.Parallel()

	for _, origin := range []OriginScope{
		{Vendor: "gcp", ID: strings.Repeat("x", maxOriginLength-len("gcp:")+1)},
		{Vendor: "vps", ID: "k3j 9x0"},
	} {
		if _, err := resolveOrigin(t.Context(), fixedOrigin(origin).Scope); err == nil {
			t.Errorf("resolveOrigin(%q) = nil error, want it refused: its records' comments would not read back or fit", origin)
		}
	}
}
