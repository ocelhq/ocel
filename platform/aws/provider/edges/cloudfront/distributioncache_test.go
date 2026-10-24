package cloudfront

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	foreignDistribution = "a site ocel did not deploy"
	foreignHost         = "taken.example.com"
)

func seedForeignDistribution(w *world) {
	w.front.distributions["EFOREIGN"] = &fakeDistribution{
		id:     "EFOREIGN",
		domain: "eforeign.cloudfront.net",
		etag:   "dist-foreign",
		config: &cftypes.DistributionConfig{
			Comment: aws.String(foreignDistribution),
			Enabled: aws.Bool(true),
			Aliases: &cftypes.Aliases{Items: []string{foreignHost}, Quantity: aws.Int32(1)},
		},
	}
}

func TestDomainOwnerListsTheAccountOnceHoweverManyUnroutedHostnamesItIsAsked(t *testing.T) {
	t.Parallel()

	w := newWorld()
	seedForeignDistribution(w)
	e := bootstrapped(t, w)
	ctx := context.Background()

	for _, hostname := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		owner, err := e.DomainOwner(ctx, hostname)
		if err != nil {
			t.Fatalf("DomainOwner(%q): %v", hostname, err)
		}
		if owner != "" {
			t.Errorf("DomainOwner(%q) = %q, want nothing serving a hostname no distribution carries", hostname, owner)
		}
	}
	owner, err := e.DomainOwner(ctx, "TAKEN.example.com")
	if err != nil {
		t.Fatalf("DomainOwner(%q): %v", foreignHost, err)
	}

	if owner != foreignDistribution {
		t.Errorf("DomainOwner(%q) = %q, want the distribution that carries it (%q)", foreignHost, owner, foreignDistribution)
	}
	if got := w.front.count("ListDistributions"); got != 1 {
		t.Errorf("ListDistributions calls = %d, want 1: one preflight asks about every declared hostname, and the account's distributions are the same for each", got)
	}
}

func TestDomainOwnerReadsAgainOnceThisEdgeChangedADistribution(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	ctx := context.Background()
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	bound(t, stack)
	if _, err := e.DomainOwner(ctx, "other.example.com"); err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}

	if err := stack.UnbindDomain(ctx, boundHost); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}
	owner, err := e.DomainOwner(ctx, boundHost)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}

	if owner != "" {
		t.Errorf("DomainOwner(%q) = %q after this edge unbound it, want nothing: the distributions it listed before the unbind still carried the alias", boundHost, owner)
	}
}
