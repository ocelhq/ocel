package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const wholeEdgeKind edge.Kind = "whole-test-edge"

type wholeEdge struct {
	edge.Edge
	stack *boundStack
	specs []edge.PreviewWildcardSpec
}

func (e *wholeEdge) Kind() edge.Kind { return wholeEdgeKind }

func (e *wholeEdge) Facts() edge.Facts { return edge.Facts{CredentialScope: "whole-acct"} }

func (e *wholeEdge) Open(edge.StackState) (edge.EdgeStack, error) { return e.stack, nil }

func (e *wholeEdge) ReconcilePreviewWildcard(_ context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	e.specs = append(e.specs, spec)
	return "whole-front.example.net", nil
}

func fakeDomainClients(ssmc *stateSSM, acmAPI *domainACM, writer edge.DNSWriter) domainClients {
	return domainClients{
		region: "eu-west-1",
		ssm:    ssmc,
		poller: dns.Poller{
			Lookup:   func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil },
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 1,
			Every:    time.Millisecond,
		},
		prober: certs.Prober{
			Get: func(context.Context, string) (http.Header, error) {
				return http.Header{http.CanonicalHeaderKey(edge.HeaderEdge): []string{string(wholeEdgeKind)}}, nil
			},
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 1,
			Every:    time.Millisecond,
			Now:      func() time.Time { return time.Unix(1755500000, 0).UTC() },
			Jitter:   func() float64 { return 0.5 },
		},
		certifierFor: func(edge.Edge) certs.Certifier {
			return certs.Certifier{Issuer: certs.Issuer{
				API:      acmAPI,
				Region:   certs.CloudFrontRegion,
				Wait:     func(context.Context, time.Duration) error { return nil },
				Attempts: 3,
				Every:    time.Millisecond,
			}}
		},
		discarderFor: func(certs.Certificate) certs.Issuer {
			return certs.Issuer{API: acmAPI, Region: certs.CloudFrontRegion}
		},
		writerFor: func(string, string) (edge.DNSWriter, error) { return writer, nil },
	}
}

func wholeServer(clients domainClients, state domains.State, front edge.Edge) *Server {
	s := &Server{stores: stores{
		openDomain: func(context.Context, string) (domainClients, error) { return clients, nil },
		openState:  func(context.Context, string, bool) (domains.State, error) { return state, nil },
	}}
	if _, err := s.memo.edgeFor(wholeEdgeKind, clients.region).resolve(func() (edge.Edge, error) {
		return front, nil
	}); err != nil {
		panic(err)
	}
	return s
}

func TestAddHostnameWhole(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	ssmc := &stateSSM{params: map[string]string{}}
	stack := &boundStack{
		name:      string(wholeEdgeKind),
		state:     edge.StackState{Slug: domainSlug},
		log:       &callLog{},
		hostFront: func(hostname string) string { return "front-of-" + hostname + ".example.net" },
	}
	state := newRecordState()
	if err := state.WriteStack(ctx, edge.ClassProduction, domainSlug, stackRecordOn(stack.state)); err != nil {
		t.Fatalf("WriteStack: %v", err)
	}
	acmAPI := newDomainACM()
	acmAPI.log = &callLog{}
	writer := &domainWriter{log: &callLog{}}
	s := wholeServer(fakeDomainClients(ssmc, acmAPI, writer), state, &wholeEdge{stack: stack})

	session, err := s.hostnameSession(ctx, hostnameRequest{
		slug:        domainSlug,
		edgeKind:    string(wholeEdgeKind),
		configured:  []string{"shop.app.com"},
		certificate: true,
	})
	if err != nil {
		t.Fatalf("hostnameSession: %v", err)
	}
	if err := session.add(ctx, func(string) {}); err != nil {
		t.Fatalf("add: %v", err)
	}

	record, err := state.ReadStack(ctx, edge.ClassProduction, domainSlug)
	if err != nil {
		t.Fatalf("ReadStack: %v", err)
	}
	recorded := record.Settlement()
	if !recorded.Ready("shop.app.com", wholeEdgeKind) {
		t.Errorf("recorded = %+v, want the host settled through the RPC-built session", recorded)
	}
	if recorded.Certificate.ARN == "" || recorded.Host("shop.app.com").Certificate != recorded.Certificate.ARN {
		t.Errorf("recorded = %+v, want the requested certificate bound to the host", recorded)
	}
	if len(stack.bound) != 1 || stack.bound[0].Hostname != "shop.app.com" {
		t.Errorf("bound = %+v, want the host bound on the edge stack", stack.bound)
	}
}

func TestUsePreviewWildcardWhole(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	ssmc := &stateSSM{params: map[string]string{
		bootstrap.EdgeCredentialsPreviewParamName: `{"accessKeyId":"AKIA","secretAccessKey":"secret"}`,
	}}
	acmAPI := newDomainACM()
	acmAPI.log = &callLog{}
	writer := &domainWriter{log: &callLog{}}
	front := &wholeEdge{stack: &boundStack{log: &callLog{}}}
	state := newRecordState()
	s := wholeServer(fakeDomainClients(ssmc, acmAPI, writer), state, front)
	if _, err := s.memo.deployedFor("", true).resolve(func() (bootstrap.Deployed, error) {
		return bootstrap.Deployed{Present: true, StateTable: "state", AssetBucket: "assets"}, nil
	}); err != nil {
		t.Fatalf("seed deployed: %v", err)
	}

	req := &contractv1.UsePreviewWildcardRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		BaseDomain: "preview.acme.com",
		Edge:       &contractv1.EdgeSelection{Kind: string(wholeEdgeKind)},
	}
	if err := s.runUsePreviewWildcard(ctx, req, func(string) {}, func(string, []edge.Record, ...string) {}, func(string) {}); err != nil {
		t.Fatalf("runUsePreviewWildcard: %v", err)
	}

	recorded, err := state.ReadWildcard(ctx, edge.ClassPreview)
	if err != nil {
		t.Fatalf("ReadWildcard: %v", err)
	}
	if recorded.BaseDomain != "preview.acme.com" || recorded.Edge != wholeEdgeKind || recorded.Scope != "whole-acct" {
		t.Errorf("recorded = %+v, want the domain, its edge and its scope written through the whole RPC", recorded)
	}
	if probe := recorded.Settled.Probe; !probe.OK || probe.Edge != wholeEdgeKind {
		t.Errorf("probe = %+v, want the wildcard proven against the edge that raised it", probe)
	}
	if len(front.specs) != 1 || front.specs[0].Certificate == "" {
		t.Errorf("specs = %+v, want the shared entry reconciled with the settled certificate", front.specs)
	}
	if recorded.Certificate.ARN == "" {
		t.Errorf("recorded = %+v, want the certificate settled and recorded", recorded)
	}
}
