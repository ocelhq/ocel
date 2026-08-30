package vps_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const domainSlug = "bound"

type overPlainHTTP struct{ at string }

func (o overPlainHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	asked := req.Clone(req.Context())
	asked.URL.Scheme = "http"
	asked.URL.Host = o.at
	asked.Host = req.URL.Hostname()
	return http.DefaultTransport.RoundTrip(asked)
}

func overTheContract(t *testing.T, p *vps.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()

	server := httptest.NewServer(providerkit.ConformanceMux(providerkit.Spec{
		Version: "live-suite",
		New: func(context.Context, providerkit.Options) (providerkit.Provider, error) {
			return p, nil
		},
	}))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() = %v", err)
	}
	return client
}

func recorded(t *testing.T, p *vps.Provider, slug string, state edge.StackState) {
	t.Helper()

	body, err := json.Marshal(providerkit.EdgeStackState{Edge: state})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Records().Write(context.Background(), providerkit.Record{
		Name:  providerkit.EdgeStackRecord(providerkit.ClassProduction, slug),
		Bytes: body,
	}); err != nil {
		t.Fatalf("record the edge stack the kit opens: %v", err)
	}
}

type owed struct {
	records []*progressv1.DnsRecord
	notes   []string
}

func drained(t *testing.T, stream *connect.ServerStreamForClient[progressv1.OperationEvent]) (owed, []string, *progressv1.ResultEvent) {
	t.Helper()
	defer stream.Close()

	var asked owed
	var said []string
	var result *progressv1.ResultEvent
	for stream.Receive() {
		event := stream.Msg()
		if dns := event.GetDnsOwed(); dns != nil {
			asked.records = append(asked.records, dns.GetRecords()...)
			asked.notes = append(asked.notes, dns.GetNotes()...)
		}
		if log := event.GetLog(); log != nil {
			said = append(said, log.GetMessage())
		}
		if done := event.GetResult(); done != nil {
			result = done
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("the stream ended in %v", err)
	}
	return asked, said, result
}

func servingTheBox(t *testing.T) (machine, *vps.Provider, contractv1connect.ProviderServiceClient, string) {
	t.Helper()

	vm, p := onABoxServingContainers(t)
	t.Cleanup(func() { closing(t, p) })
	p.Probing(&http.Client{Transport: overPlainHTTP{at: vm.addr + ":80"}})

	opened, err := p.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", boxedge.Kind, err)
	}
	stack, err := opened.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: domainSlug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	promotes(t, stack, "p-one", "one", standsUp(t, p, "one"), 1)
	recorded(t, p, domainSlug, stack.State())

	return vm, p, overTheContract(t, p), liveHostname
}

func (vm machine) asks(t *testing.T, hostname, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.peers(t, "curl -sS -m 10 -H "+quote("Host: "+hostname)+
		" http://"+host.ProxyContainer+path))
}

func (vm machine) heads(t *testing.T, hostname string) string {
	t.Helper()
	return vm.peers(t, "curl -sS -m 10 -o /dev/null -D - -H "+quote("Host: "+hostname)+
		" http://"+host.ProxyContainer+"/")
}

func TestLiveDomainAddOwesAnARecordNamingTheBoxAndTheBoxThenServesTheHostname(t *testing.T) {
	vm, p, client, hostname := servingTheBox(t)

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       domainSlug,
		Configured: []string{hostname},
		Edge:       &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	})
	if err != nil {
		t.Fatalf("AddHostname() = %v", err)
	}
	asked, _, result := drained(t, stream)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v, want the hostname settled", result.GetError())
	}

	address, err := p.Host().Address(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(asked.records) != 1 {
		t.Fatalf("the run owed %d records for one hostname: %v", len(asked.records), asked.records)
	}
	owedRecord := asked.records[0]
	if owedRecord.GetType() != string(edge.RecordTypeA) || owedRecord.GetValue() != address || owedRecord.GetName() != hostname {
		t.Errorf("the record owed is %s %s %s, want an A record pointing %s at the address of the machine the user bought",
			owedRecord.GetName(), owedRecord.GetType(), owedRecord.GetValue(), hostname)
	}
	if owedRecord.GetProxied() {
		t.Error("the owed record asks for a proxied record, and a box is answered by its own address")
	}
	if !strings.Contains(strings.Join(asked.notes, "\n"), "wrote none of them") {
		t.Errorf("the owed records came with %v, want the run to say ocel wrote none of them: nothing here writes DNS unless a writer is configured", asked.notes)
	}

	if served := vm.asks(t, hostname, "/"); served != "one" {
		t.Errorf("the box answered %q for the hostname it just bound, want the release the project serves", served)
	}
	if head := vm.heads(t, hostname); !strings.Contains(strings.ToLower(head), strings.ToLower(edge.HeaderEdge)+": "+host.EdgeName) {
		t.Errorf("the box answered the bound hostname with\n%s\nwant %s: %s, which is what the settle reads to decide this edge serves it",
			head, edge.HeaderEdge, host.EdgeName)
	}
}

func TestLiveDomainStatusNamesTheRecordsOwedTheCertificateHandleAndWhoRenewsIt(t *testing.T) {
	_, _, client, hostname := servingTheBox(t)

	req := &contractv1.HostnameRequest{
		Slug:       domainSlug,
		Configured: []string{hostname},
		Edge:       &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	}
	stream, err := client.AddHostname(context.Background(), req)
	if err != nil {
		t.Fatalf("AddHostname() = %v", err)
	}
	if _, _, result := drained(t, stream); result == nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v, want the hostname settled", result.GetError())
	}

	resp, err := client.GetHostnameStatus(context.Background(), req)
	if err != nil {
		t.Fatalf("GetHostnameStatus() = %v", err)
	}
	if len(resp.GetHostnames()) != 1 {
		t.Fatalf("the status carries %d rows for one declared hostname: %v", len(resp.GetHostnames()), resp.GetHostnames())
	}
	row := resp.GetHostnames()[0]
	if row.GetHostname() != hostname || !row.GetDeclared() {
		t.Errorf("the status row is %+v, want the declared hostname", row)
	}
	if want := certs.ProxyHandle(hostname); row.GetCertificate().GetCertificateId() != want {
		t.Errorf("the certificate handle is %q, want %q: the proxy on this box obtained it and ocel placed no key material here",
			row.GetCertificate().GetCertificateId(), want)
	}
	if row.GetRenewalStatus() != certs.ProxyRenewal {
		t.Errorf("the renewal line reads %q, want %q: `nothing renews this one` is a thing the user must be able to read rather than infer",
			row.GetRenewalStatus(), certs.ProxyRenewal)
	}
	if len(row.GetCertificate().GetRecordsOwed()) == 0 {
		t.Error("the status owes no record for a hostname nothing here writes DNS for, so the one thing the user still has to do goes unsaid")
	}
	if len(row.GetCertificate().GetRecordsWritten()) != 0 {
		t.Errorf("the status reports %v as written, and ocel wrote nothing", row.GetCertificate().GetRecordsWritten())
	}
}

func TestLiveDomainRmStopsTheHostnameServingAndLeavesNothingOfItLoaded(t *testing.T) {
	vm, p, client, hostname := servingTheBox(t)

	bound := &contractv1.HostnameRequest{
		Slug:       domainSlug,
		Configured: []string{hostname},
		Edge:       &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	}
	stream, err := client.AddHostname(context.Background(), bound)
	if err != nil {
		t.Fatalf("AddHostname() = %v", err)
	}
	if _, _, result := drained(t, stream); result == nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v, want the hostname settled", result.GetError())
	}

	gone, err := client.RemoveHostname(context.Background(), &contractv1.HostnameRequest{
		Slug: domainSlug,
		Host: hostname,
		Edge: &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	})
	if err != nil {
		t.Fatalf("RemoveHostname() = %v", err)
	}
	if _, _, result := drained(t, gone); result == nil || !result.GetSuccess() {
		t.Fatalf("RemoveHostname() = %v, want the hostname given back", result.GetError())
	}

	if head := vm.heads(t, hostname); !strings.Contains(head, "404") {
		t.Errorf("the box answered an unbound hostname with\n%s\nwant the bare 404 every hostname nothing claims falls to", head)
	}
	loaded := vm.ssh(t, "sudo cat "+quote(host.ProxyConfig))
	if strings.Contains(loaded, hostname) {
		t.Errorf("%s still names %s after the unbind:\n%s", host.ProxyConfig, hostname, loaded)
	}
	owner, err := p.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := owner.DomainOwner(context.Background(), hostname); err != nil || held != "" {
		t.Errorf("DomainOwner(%s) = %q, %v, want nothing claiming a hostname the project gave back", hostname, held, err)
	}
}

func TestLiveTheCertificateBehindAnUnboundHostnameStaysOnTheBox(t *testing.T) {
	_, p, client, hostname := servingTheBox(t)

	req := &contractv1.HostnameRequest{
		Slug:       domainSlug,
		Configured: []string{hostname},
		Edge:       &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	}
	stream, err := client.AddHostname(context.Background(), req)
	if err != nil {
		t.Fatalf("AddHostname() = %v", err)
	}
	if _, _, result := drained(t, stream); result == nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v, want the hostname settled", result.GetError())
	}

	gone, err := client.RemoveHostname(context.Background(), &contractv1.HostnameRequest{
		Slug: domainSlug,
		Host: hostname,
		Edge: &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	})
	if err != nil {
		t.Fatalf("RemoveHostname() = %v", err)
	}
	if _, _, result := drained(t, gone); result == nil || !result.GetSuccess() {
		t.Fatalf("RemoveHostname() = %v, want the hostname given back", result.GetError())
	}

	held := providerkit.Certificate{ID: certs.ProxyHandle(hostname)}
	if err := p.DiscardCertificate(context.Background(), held, edge.DiscardReporter()); err != nil {
		t.Errorf("DiscardCertificate(%s) = %v, want nil: ocel places no key material on a box so it holds authority to remove none, and the retained certificate is what makes a re-bind free against the CA's per-week ceiling",
			held.ID, err)
	}
}

func TestLiveASecondProjectDeclaringAServedHostnameIsNamedAsAClaimAtPreflight(t *testing.T) {
	_, _, client, hostname := servingTheBox(t)

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       domainSlug,
		Configured: []string{hostname},
		Edge:       &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	})
	if err != nil {
		t.Fatalf("AddHostname() = %v", err)
	}
	if _, _, result := drained(t, stream); result == nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v, want the hostname settled", result.GetError())
	}

	other, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "intruder",
		Domains:      []string{hostname, "nobody.example.invalid"},
		Edge:         &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	})
	if err != nil {
		t.Fatalf("Preflight() = %v", err)
	}
	claims := other.GetDomainClaims()
	if len(claims) != 2 {
		t.Fatalf("the preflight named %d claims for two declared hostnames: %v", len(claims), claims)
	}
	if claims[0].GetStatus() != contractv1.DomainClaim_STATUS_CLAIMED {
		t.Errorf("%s reads as %v to a second project on the same box, want it claimed: the proxy's route table is box-wide, so deploying over it would take a live site off the air",
			hostname, claims[0].GetStatus())
	}
	if want := boxedge.Surface(domainSlug, edge.ClassProduction); claims[0].GetOwner() != want {
		t.Errorf("the claim on %s names %q, want %q: the refusal has to name who holds it or there is nothing to act on",
			hostname, claims[0].GetOwner(), want)
	}
	if claims[1].GetStatus() != contractv1.DomainClaim_STATUS_UNCLAIMED || claims[1].GetOwner() != "" {
		t.Errorf("a hostname nothing on this box serves reads as %+v, want it unclaimed", claims[1])
	}

	mine, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         domainSlug,
		Domains:      []string{hostname},
		Edge:         &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	})
	if err != nil {
		t.Fatalf("Preflight() = %v", err)
	}
	if held := mine.GetDomainClaims(); len(held) != 1 || held[0].GetStatus() != contractv1.DomainClaim_STATUS_UNCLAIMED {
		t.Errorf("the project that bound %s reads its own hostname as %v, want it unclaimed: every redeploy after a `domain add` would otherwise be refused for holding its own domain",
			hostname, held)
	}
}
