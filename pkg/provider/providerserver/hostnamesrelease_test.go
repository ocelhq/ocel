package providerserver

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type originlessRouter struct{ router.Router }

func (originlessRouter) Hooks() router.Hooks { return router.Hooks{} }

func TestAHostnameWhoseAnsweringRoutersOriginCannotBeReadKeepsItsOriginCertificate(t *testing.T) {
	t.Parallel()
	edges := fake.NewEdges()
	relay := edges.Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	front, err := edges.Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := front.Open(edge.StackState{Slug: "shop", Tier: environment.TierProduction})
	if err != nil {
		t.Fatal(err)
	}
	session := &edgeSession{
		sharedStack: &sharedStack{
			front:   front,
			routers: map[router.Kind]pairedRouter{"previous": {Router: originlessRouter{}}},
			current: stack,
			states:  map[router.Kind]router.StackState{},
		},
		store: edgeStateStore{keyValues: fake.NewKeyValues(), name: stackrecords.EdgeStackKey(environment.TierProduction, "shop")},
	}
	rows := &hostnames{edgeSession: session}
	host := stackrecords.HostnameState{PreviousRouter: "previous", OriginCertificateID: "origin-certificate-1"}

	if err := rows.releasePreviousRouter(context.Background(), "app.acme.com", &host, "answering", progress.Discard()); err != nil {
		t.Fatalf("releasePreviousRouter = %v", err)
	}

	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, nil) {
		t.Errorf("revoked %v, want nothing: the answering router's origin could not be read, so it may still answer with the certificate", revoked)
	}
	if host.OriginCertificateID != "origin-certificate-1" {
		t.Errorf("the hostname records origin certificate %q, want origin-certificate-1 kept", host.OriginCertificateID)
	}
}
