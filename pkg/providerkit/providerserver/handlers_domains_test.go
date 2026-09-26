package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func deployed(t *testing.T, provider *fake.Provider, class edge.Class, slug string) {
	t.Helper()
	seedStack(t, provider, class, slug, stackrecords.EdgeState{
		Edge: edge.StackState{Slug: slug, Class: class, Endpoint: "https://" + slug + ".fake.invalid"},
	})
	promoted(t, provider, class, slug)
}

func promoted(t *testing.T, provider *fake.Provider, class edge.Class, slug string) {
	t.Helper()
	promotion := edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "d1"}}
	if err := ledger.New(provider.Records(), class, slug).Promote(context.Background(), promotion, "", edge.DiscardProgress()); err != nil {
		t.Fatal(err)
	}
}

func seedStack(t *testing.T, provider *fake.Provider, class edge.Class, slug string, state stackrecords.EdgeState) {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	name := stackrecords.EdgeStackRecord(class, slug)
	held, err := records.ReadOrEmpty(context.Background(), provider.Records(), name)
	if err != nil {
		t.Fatal(err)
	}
	held.Bytes = encoded
	if _, err := provider.Records().Write(context.Background(), held); err != nil {
		t.Fatal(err)
	}
}

func readStack(t *testing.T, provider *fake.Provider, class edge.Class, slug string) stackrecords.EdgeState {
	t.Helper()
	held, err := records.ReadOrEmpty(context.Background(), provider.Records(), stackrecords.EdgeStackRecord(class, slug))
	if err != nil {
		t.Fatal(err)
	}
	var state stackrecords.EdgeState
	if len(held.Bytes) > 0 {
		if err := json.Unmarshal(held.Bytes, &state); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func zoned(zone string) *contractv1.EdgeSelection {
	return &contractv1.EdgeSelection{Dns: &contractv1.Dns{Kind: string(fake.KindZone), Zone: zone}}
}

func TestAddHostnameBindsWritesAndRecordsTheProbe(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want it to settle the hostname", result.GetError())
	}

	state := readStack(t, provider, edge.ClassProduction, "shop")
	if !slices.Contains(state.Edge.Bound, "app.acme.com") {
		t.Errorf("the recorded edge state binds %v, want app.acme.com among them", state.Edge.Bound)
	}
	settled := state.Host("app.acme.com")
	if !settled.Probe.OK || settled.Probe.Edge != fake.KindRelay {
		t.Errorf("recorded probe = %+v, want it answered by the %s edge", settled.Probe, fake.KindRelay)
	}
	if len(settled.Written) == 0 {
		t.Error("the settlement records no written DNS record, though a writer was selected")
	}
	if written := provider.DNS().(*fake.DNS).Zone("acme.com").Records(); len(written) != 1 {
		t.Errorf("the zone holds %v, want the one record pointing app.acme.com at the edge", written)
	}
}

func TestAddHostnameSaysWhatTheEdgeAsksOfYouAsItBinds(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).SayOnBind("Route app.acme.com → http://127.0.0.1:8480 (keep Host, set X-Forwarded-Proto)")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	var said []string
	for stream.Receive() {
		said = append(said, stream.Msg().GetProgress().GetMessage())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(said, func(line string) bool { return strings.HasPrefix(line, "Route app.acme.com") }) {
		t.Errorf("AddHostname() said %v, want the route the edge asks of you", said)
	}
}

func TestAddHostnameOnAProjectThatPromotedNothingSaysNothingServesIt(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	seedStack(t, provider, edge.ClassProduction, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{Slug: "shop", Class: edge.ClassProduction, Endpoint: "https://shop.fake.invalid"},
	})

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	said := result.GetError() + connectMessage(err)
	if result.GetSuccess() || !strings.Contains(said, "promoted no release") || strings.Contains(said, "deploy again") {
		t.Fatalf("AddHostname() = %q, want it to say plainly that nothing is promoted for app.acme.com to serve", said)
	}
	held := readStack(t, provider, edge.ClassProduction, "shop")
	if len(held.Edge.Bound) != 0 || len(held.Hosts) != 0 {
		t.Errorf("the refused add left the edge binding %v and the state settling %v, want nothing changed: `ocel deploy` settles the hostname when it promotes",
			held.Edge.Bound, held.Hostnames())
	}
	if bound := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings(); len(bound) != 0 {
		t.Errorf("the refused add bound %v, want nothing bound", bound)
	}
	writer, err := provider.DNS().Open(fake.KindZone, "acme.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if written := writer.(*fake.DNSRecords).Records(); len(written) != 0 {
		t.Errorf("the refused add wrote %v, want no record written", written)
	}
}

func TestAddHostnameOwesTheRecordsWhenNoWriterIsSelected(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	var owed, notes []string
	defer stream.Close()
	for stream.Receive() {
		if event := stream.Msg().GetDnsOwed(); event != nil {
			for _, record := range event.GetRecords() {
				owed = append(owed, record.GetName())
			}
			notes = append(notes, event.GetNotes()...)
		}
	}
	if !slices.Contains(owed, "app.acme.com") {
		t.Errorf("the run owed %v, want app.acme.com asked of the operator", owed)
	}
	if !slices.ContainsFunc(notes, func(note string) bool { return strings.Contains(note, "wrote none of them") }) {
		t.Errorf("the owed records came with %v, want a note saying ocel wrote none of them: instructions-only DNS is the normal state and the text may never leave the operator guessing whether something was automated", notes)
	}
	if settled := readStack(t, provider, edge.ClassProduction, "shop").Host("app.acme.com"); len(settled.Owed) == 0 {
		t.Error("the settlement owes no record, so nothing tells the operator what to write")
	}
}

func TestAddHostnameRefusesAHostTheProjectDoesNotDeclare(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Host:       "other.acme.com",
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	if _, err := drain(stream); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("AddHostname() = %v, want it refused as an invalid argument", err)
	}
}

func TestAddHostnameRefusesAProjectWithNoProductionDeploy(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetSuccess() {
		t.Fatal("AddHostname() settled a hostname against a project that has never deployed")
	}
}

func TestGetHostnameStatusReportsWhatIsPending(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	before, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
	})
	if err != nil {
		t.Fatalf("GetHostnameStatus() error = %v", err)
	}
	if before.GetReady() {
		t.Error("GetHostnameStatus() called an unbound hostname ready")
	}
	if len(before.GetHostnames()) != 1 || before.GetHostnames()[0].GetPending() == "" {
		t.Errorf("GetHostnameStatus() = %+v, want one row saying what is pending", before.GetHostnames())
	}

	settle, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(settle); err != nil {
		t.Fatal(err)
	}

	after, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("GetHostnameStatus() error = %v", err)
	}
	if !after.GetReady() {
		t.Errorf("GetHostnameStatus() = %+v, want a settled hostname reported ready", after.GetHostnames())
	}
	row := after.GetHostnames()[0]
	if row.GetServingPointer() != string(fake.KindRelay) {
		t.Errorf("serving pointer = %q, want %q", row.GetServingPointer(), fake.KindRelay)
	}
	if len(row.GetCertificate().GetRecordsWritten()) == 0 {
		t.Error("the status names no written record, though the zone holds one")
	}
}

func TestRemoveHostnameUnbindsAndReleasesItsRecords(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	add, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(add); err != nil {
		t.Fatal(err)
	}

	remove, err := client.RemoveHostname(context.Background(), &contractv1.HostnameRequest{
		Slug: "shop",
		Host: "app.acme.com",
		Edge: zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("RemoveHostname() error = %v", err)
	}
	result, err := drain(remove)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveHostname() = %q, want the hostname given back", result.GetError())
	}

	state := readStack(t, provider, edge.ClassProduction, "shop")
	if slices.Contains(state.Edge.Bound, "app.acme.com") {
		t.Errorf("the recorded edge state still binds %v", state.Edge.Bound)
	}
	if len(state.Hosts) != 0 {
		t.Errorf("the settlement still holds %v", state.Hostnames())
	}
	if written := provider.DNS().(*fake.DNS).Zone("acme.com").Records(); len(written) != 0 {
		t.Errorf("the zone still holds %v, want the records ocel wrote taken back", written)
	}
}

func TestRemoveHostnameFinishesOverAnUnbindThatOnlyWarnsAndSaysWhat(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	add, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(add); err != nil {
		t.Fatal(err)
	}
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).WarnOnUnbind(errors.New("its buckets still answer app.acme.com until the next deploy"))

	remove, err := client.RemoveHostname(context.Background(), &contractv1.HostnameRequest{
		Slug: "shop",
		Host: "app.acme.com",
		Edge: zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("RemoveHostname() error = %v", err)
	}
	var said []string
	var result *progressv1.ResultEvent
	for remove.Receive() {
		said = append(said, remove.Msg().GetProgress().GetMessage())
		if done := remove.Msg().GetResult(); done != nil {
			result = done
		}
	}
	if err := remove.Err(); err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveHostname() = %q, want the hostname given back: the edge released it and only warned about what the next deploy repairs", result.GetError())
	}
	if !slices.ContainsFunc(said, func(line string) bool { return strings.Contains(line, "next deploy") }) {
		t.Errorf("RemoveHostname() said %v, want the edge's warning passed on", said)
	}
	if state := readStack(t, provider, edge.ClassProduction, "shop"); len(state.Hosts) != 0 {
		t.Errorf("the settlement still holds %v", state.Hostnames())
	}
}

func TestRemoveHostnameRefusesAHostTheProjectDoesNotServe(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	stream, err := client.RemoveHostname(context.Background(), &contractv1.HostnameRequest{
		Slug: "shop",
		Host: "app.acme.com",
	})
	if err != nil {
		t.Fatalf("RemoveHostname() error = %v", err)
	}
	if _, err := drain(stream); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("RemoveHostname() = %v, want it refused as an invalid argument", err)
	}
}

func TestAddHostnameBindsTheCertificateItsProviderSettles(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	provider.Pin("app.acme.com", "cert-for-app")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	if _, err := drain(stream); err != nil {
		t.Fatal(err)
	}

	bindings := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings()
	if len(bindings) != 1 || bindings[0].Certificate != "cert-for-app" {
		t.Errorf("the edge was bound with %+v, want the certificate the provider settled", bindings)
	}
	if settled := readStack(t, provider, edge.ClassProduction, "shop").Host("app.acme.com"); settled.Certificate.ID != "cert-for-app" {
		t.Errorf("recorded certificate = %q, want the one the provider settled", settled.Certificate.ID)
	}
}

func TestAddHostnameRefusesWhenNoCertificateCanBeSettled(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	provider.RefuseCertificates(refusal.Refuse(refusal.CodeNotReady, "no certificate covers app.acme.com"))

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetSuccess() {
		t.Fatal("AddHostname() bound a hostname with no certificate to serve it with")
	}
	if bindings := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings(); len(bindings) != 0 {
		t.Errorf("the edge was bound with %+v, want the run refused before it bound anything", bindings)
	}
}

var validationRecord = edge.Record{
	Name:  "_ocel.app.acme.com",
	Type:  edge.RecordTypeCNAME,
	Value: "_target.validations.invalid",
}

func TestAddHostnameSettlesTheValidationRecordsItsProviderProves(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	provider.IssueCertificates(validationRecord)

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want the certificate issued and the hostname settled", result.GetError())
	}

	settled := readStack(t, provider, edge.ClassProduction, "shop").Host("app.acme.com")
	if !settled.Certificate.Requested || settled.Certificate.ID != "issued-for-app.acme.com" {
		t.Errorf("recorded certificate = %+v, want the one ocel requested", settled.Certificate)
	}
	if !slices.Contains(settled.Certificate.Written, validationRecord) {
		t.Errorf("the certificate records %v as written, want the validation record among them", settled.Certificate.Written)
	}
	if written := provider.DNS().(*fake.DNS).Zone("acme.com").Records(); len(written) != 2 {
		t.Errorf("the zone holds %v, want the validation record beside the one pointing at the edge", written)
	}
}

func TestAddHostnameDiscardsTheCertificateItSupersedes(t *testing.T) {
	t.Parallel()
	client, p := contractServed(t, "1.0.0")
	stale := edge.Record{Name: "_stale.app.acme.com", Type: edge.RecordTypeCNAME, Value: "_stale.validations.invalid"}
	seedStack(t, p, edge.ClassProduction, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{Slug: "shop", Class: edge.ClassProduction, Endpoint: "https://shop.fake.invalid"},
		Hosts: map[string]stackrecords.Settled{
			"app.acme.com": {Certificate: provider.Certificate{ID: "superseded", Requested: true, Written: []edge.Record{stale}}},
		},
	})
	promoted(t, p, edge.ClassProduction, "shop")
	writer, err := p.DNS().Open(fake.KindZone, "acme.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Ensure(context.Background(), []edge.Record{stale}, nil); err != nil {
		t.Fatal(err)
	}
	p.IssueCertificates(validationRecord)

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v %q, want the hostname settled", err, result.GetError())
	}

	if discarded := p.Discarded(); !slices.Contains(discarded, "superseded") {
		t.Errorf("the provider discarded %v, want the superseded certificate among them", discarded)
	}
	if held := writer.(*fake.DNSRecords).Records(); slices.Contains(held, stale) {
		t.Errorf("the zone still holds %v, want the superseded validation record released", held)
	}
}

var rotatedValidationRecord = edge.Record{
	Name:  "_ocel-again.app.acme.com",
	Type:  edge.RecordTypeCNAME,
	Value: "_rotated.validations.invalid",
}

func addHostname(t *testing.T, client contractv1connect.ProviderServiceClient) *progressv1.ResultEvent {
	t.Helper()
	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	return result
}

func TestAddHostnameDiscardsTheSupersededCertificateOnlyOnceTheRebindFreesIt(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	provider.IssueCertificates(validationRecord)
	if result := addHostname(t, client); !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want the hostname settled", result.GetError())
	}

	provider.RotateCertificates()
	provider.IssueCertificates(rotatedValidationRecord)
	provider.RefuseDiscardingAServingCertificate(errors.New("the certificate is still bound to the edge"))
	if result := addHostname(t, client); !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want the rotation settled with the superseded certificate discarded after the rebind", result.GetError())
	}

	if discarded := provider.Discarded(); !slices.Contains(discarded, "issued-for-app.acme.com") {
		t.Errorf("the provider discarded %v, want the superseded certificate among them", discarded)
	}
	settled := readStack(t, provider, edge.ClassProduction, "shop").Host("app.acme.com")
	if len(settled.Superseded) != 0 {
		t.Errorf("the record still carries %+v, want the discarded certificate forgotten", settled.Superseded)
	}
	if held := provider.DNS().(*fake.DNS).Zone("acme.com").Records(); slices.Contains(held, validationRecord) {
		t.Errorf("the zone still holds %v, want the superseded validation record released", held)
	}
}

func TestAddHostnameKeepsTheSupersededCertificateOnRecordWhileItsReplacementIsPending(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	provider.IssueCertificates(validationRecord)
	if result := addHostname(t, client); !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want the hostname settled", result.GetError())
	}

	provider.RotateCertificates()
	provider.IssueCertificates(rotatedValidationRecord)
	provider.StallAfterProving(refusal.Refuse(refusal.CodeNotReady, "the certificate is still validating"))
	if result := addHostname(t, client); result.GetSuccess() {
		t.Fatal("AddHostname() settled the hostname, want it told to come back to a certificate still validating")
	}

	settled := readStack(t, provider, edge.ClassProduction, "shop").Host("app.acme.com")
	if len(settled.Superseded) != 1 || settled.Superseded[0].ID != "issued-for-app.acme.com" {
		t.Fatalf("the record carries %+v superseded, want the certificate the pending one replaced still reachable", settled.Superseded)
	}
	if !slices.Contains(settled.Superseded[0].Written, validationRecord) {
		t.Errorf("the superseded certificate records %v as written, want its validation record still reachable",
			settled.Superseded[0].Written)
	}

	provider.StallAfterProving(nil)
	if result := addHostname(t, client); !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want the re-run to settle it", result.GetError())
	}
	if discarded := provider.Discarded(); !slices.Contains(discarded, "issued-for-app.acme.com") {
		t.Errorf("the provider discarded %v, want the certificate the re-run superseded released too", discarded)
	}
}

func TestAddHostnameRebindsAServedHostnameWhoseCertificateChanged(t *testing.T) {
	t.Parallel()
	client, p := contractServed(t, "1.0.0")
	seedStack(t, p, edge.ClassProduction, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{
			Slug:     "shop",
			Class:    edge.ClassProduction,
			Endpoint: "https://shop.fake.invalid",
			Front:    "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]stackrecords.Settled{
			"app.acme.com": {
				Certificate: provider.Certificate{ID: "cert-of-yesterday"},
				Probe:       stackrecords.Probe{OK: true, Edge: fake.KindRelay},
			},
		},
	})
	promoted(t, p, edge.ClassProduction, "shop")
	p.Pin("app.acme.com", "cert-of-today")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v %q, want the hostname settled again", err, result.GetError())
	}

	bindings := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings()
	if len(bindings) == 0 || bindings[len(bindings)-1].Certificate != "cert-of-today" {
		t.Errorf("the edge was bound with %+v, want the hostname rebound with the certificate it is served with now", bindings)
	}
}

func TestHostnameStatusReportsWhatTheProviderSaysOfTheCertificate(t *testing.T) {
	t.Parallel()
	client, p := contractServed(t, "1.0.0")
	seedStack(t, p, edge.ClassProduction, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{
			Slug:     "shop",
			Class:    edge.ClassProduction,
			Endpoint: "https://shop.fake.invalid",
			Front:    "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]stackrecords.Settled{
			"app.acme.com": {
				Certificate: provider.Certificate{ID: "pending-cert", Requested: true},
				Probe:       stackrecords.Probe{OK: true, Edge: fake.KindRelay},
			},
		},
	})
	expiry := time.Now().Add(24 * time.Hour)
	p.ReportCertificate(provider.CertificateHealth{
		Terminates:   true,
		Status:       "PENDING_VALIDATION",
		Domains:      []string{"other.acme.com"},
		Renewal:      "PENDING_AUTO_RENEWAL",
		ExpiresAt:    expiry.Unix(),
		ExpiringSoon: true,
	})

	status, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("GetHostnameStatus() error = %v", err)
	}
	row := status.GetHostnames()[0]
	if got := row.GetCertificate().GetCertificateStatus(); got != "PENDING_VALIDATION" {
		t.Errorf("certificate status = %q, want the state the provider reports", got)
	}
	if row.GetRenewalStatus() != "PENDING_AUTO_RENEWAL" || row.GetExpiresAt() != expiry.Unix() || !row.GetExpiringSoon() {
		t.Errorf("renewal = %+v, want the expiry and renewal the provider reports", row)
	}
	if !strings.Contains(row.GetPending(), "not issued") || row.GetReady() {
		t.Errorf("pending = %q, want a hostname whose certificate is not issued held back", row.GetPending())
	}
}

func TestGetHostnameStatusReadsTheRecordedProbeUnlessAskedToCheckLive(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	seedStack(t, provider, edge.ClassProduction, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{
			Slug:     "shop",
			Class:    edge.ClassProduction,
			Endpoint: "https://shop.fake.invalid",
			Bound:    []string{"app.acme.com"},
			Fronts:   map[string]string{"app.acme.com": "shop.relay.fake.invalid"},
		},
		Hosts: map[string]stackrecords.Settled{
			"app.acme.com": {Probe: stackrecords.Probe{At: 1755500000, OK: true, Edge: fake.KindRelay}},
		},
	})

	listed, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("GetHostnameStatus() error = %v", err)
	}
	if at := listed.GetHostnames()[0].GetCertificate().GetLastProbeAt(); at != 1755500000 {
		t.Errorf("last probe on a listing = %d, want the recorded %d: a listing reads what the last settle wrote, and reaching for every declared hostname over the network turns `ocel domain ls` into a wait as long as the timeout times the hostnames", at, 1755500000)
	}

	checked, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
		Edge:       zoned("acme.com"),
		Probe:      true,
	})
	if err != nil {
		t.Fatalf("GetHostnameStatus() error = %v", err)
	}
	if at := checked.GetHostnames()[0].GetCertificate().GetLastProbeAt(); at == 1755500000 {
		t.Errorf("last probe when the caller asked to check live = %d, want a fresh reading: `ocel domain status` is the acceptance test for a bind and answering it from the record would report a hostname as serving long after it stopped", at)
	}
}

func configuredHosts(named ...string) []*contractv1.ConfiguredHostname {
	wired := make([]*contractv1.ConfiguredHostname, 0, len(named))
	for _, host := range named {
		wired = append(wired, &contractv1.ConfiguredHostname{Hostname: host})
	}
	return wired
}

func TestTheAppAHostnameWasDeclaredUnderReachesTheEdgeThatBindsIt(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug: "shop",
		Configured: []*contractv1.ConfiguredHostname{
			{Hostname: "api.acme.com", App: "api"},
			{Hostname: "acme.com"},
		},
		Edge: zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want both hostnames settled", result.GetError())
	}

	bound := map[string]string{}
	for _, binding := range provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings() {
		bound[binding.Hostname] = binding.App
	}
	if bound["api.acme.com"] != "api" {
		t.Errorf("api.acme.com was bound naming app %q, want \"api\": the project declared it under that app, and an edge told nothing cannot point one hostname at one of the apps a project runs", bound["api.acme.com"])
	}
	if app, ok := bound["acme.com"]; !ok || app != "" {
		t.Errorf("acme.com was bound naming app %q, want none: the project declared it project-wide, and what that means is the edge's to decide — API Gateway path-routes it to every app and attributing it to one would be wrong", app)
	}
}

func TestGetHostnameStatusAnswersBeforeAProjectHasEverDeployed(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	answered, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts("app.acme.com"),
	})
	if err != nil {
		t.Fatalf("GetHostnameStatus() before the first deploy = %v, want the declared hostnames reported as unbound: `ocel doctor` asks this of every bootstrapped project, and the state between `ocel bootstrap` and the first `ocel deploy` is the ordinary one", err)
	}
	if answered.GetReady() {
		t.Error("GetHostnameStatus() called a hostname of a project that has never deployed ready")
	}
	if len(answered.GetHostnames()) != 1 || answered.GetHostnames()[0].GetHostname() != "app.acme.com" {
		t.Errorf("GetHostnameStatus() = %+v, want the one hostname the project declares", answered.GetHostnames())
	}
	if !answered.GetHostnames()[0].GetDeclared() {
		t.Error("GetHostnameStatus() reports the declared hostname as undeclared, so `ocel domain status` would print nothing about the one hostname the config names")
	}
}
