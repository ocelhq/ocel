package providerkit_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func deployed(t *testing.T, provider *fake.Provider, class providerkit.Class, slug string) {
	t.Helper()
	seedStack(t, provider, class, slug, providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: slug, Class: class, Endpoint: "https://" + slug + ".fake.invalid"},
	})
}

func seedStack(t *testing.T, provider *fake.Provider, class providerkit.Class, slug string, state providerkit.EdgeStackState) {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	name := providerkit.EdgeStackRecord(class, slug)
	held, err := providerkit.Held(context.Background(), provider.Records(), name)
	if err != nil {
		t.Fatal(err)
	}
	held.Bytes = encoded
	if _, err := provider.Records().Write(context.Background(), held); err != nil {
		t.Fatal(err)
	}
}

func readStack(t *testing.T, provider *fake.Provider, class providerkit.Class, slug string) providerkit.EdgeStackState {
	t.Helper()
	held, err := providerkit.Held(context.Background(), provider.Records(), providerkit.EdgeStackRecord(class, slug))
	if err != nil {
		t.Fatal(err)
	}
	var state providerkit.EdgeStackState
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
	deployed(t, provider, providerkit.ClassProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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

	state := readStack(t, provider, providerkit.ClassProduction, "shop")
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
	if written := provider.DNS().(*fake.DNS).Writer("acme.com").Records(); len(written) != 1 {
		t.Errorf("the zone holds %v, want the one record pointing app.acme.com at the edge", written)
	}
}

func TestAddHostnameOwesTheRecordsWhenNoWriterIsSelected(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	var owed []string
	defer stream.Close()
	for stream.Receive() {
		if event := stream.Msg().GetDnsOwed(); event != nil {
			for _, record := range event.GetRecords() {
				owed = append(owed, record.GetName())
			}
		}
	}
	if !slices.Contains(owed, "app.acme.com") {
		t.Errorf("the run owed %v, want app.acme.com asked of the operator", owed)
	}
	if settled := readStack(t, provider, providerkit.ClassProduction, "shop").Host("app.acme.com"); len(settled.Owed) == 0 {
		t.Error("the settlement owes no record, so nothing tells the operator what to write")
	}
}

func TestAddHostnameRefusesAHostTheProjectDoesNotDeclare(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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
		Configured: []string{"app.acme.com"},
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
	deployed(t, provider, providerkit.ClassProduction, "shop")

	before, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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
		Configured: []string{"app.acme.com"},
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
		Configured: []string{"app.acme.com"},
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
	deployed(t, provider, providerkit.ClassProduction, "shop")

	add, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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

	state := readStack(t, provider, providerkit.ClassProduction, "shop")
	if slices.Contains(state.Edge.Bound, "app.acme.com") {
		t.Errorf("the recorded edge state still binds %v", state.Edge.Bound)
	}
	if len(state.Hosts) != 0 {
		t.Errorf("the settlement still holds %v", state.Hostnames())
	}
	if written := provider.DNS().(*fake.DNS).Writer("acme.com").Records(); len(written) != 0 {
		t.Errorf("the zone still holds %v, want the records ocel wrote taken back", written)
	}
}

func TestRemoveHostnameRefusesAHostTheProjectDoesNotServe(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")

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
	deployed(t, provider, providerkit.ClassProduction, "shop")
	provider.Pin("app.acme.com", "cert-for-app")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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
	if settled := readStack(t, provider, providerkit.ClassProduction, "shop").Host("app.acme.com"); settled.Certificate.ARN != "cert-for-app" {
		t.Errorf("recorded certificate = %q, want the one the provider settled", settled.Certificate.ARN)
	}
}

func TestAddHostnameRefusesWhenNoCertificateCanBeSettled(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")
	provider.RefuseCertificates(providerkit.Refuse(providerkit.CodeNotReady, "no certificate covers app.acme.com"))

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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
	deployed(t, provider, providerkit.ClassProduction, "shop")
	provider.IssueCertificates(validationRecord)

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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

	settled := readStack(t, provider, providerkit.ClassProduction, "shop").Host("app.acme.com")
	if !settled.Certificate.Requested || settled.Certificate.ARN != "issued-for-app.acme.com" {
		t.Errorf("recorded certificate = %+v, want the one ocel requested", settled.Certificate)
	}
	if !slices.Contains(settled.Certificate.Written, validationRecord) {
		t.Errorf("the certificate records %v as written, want the validation record among them", settled.Certificate.Written)
	}
	if written := provider.DNS().(*fake.DNS).Writer("acme.com").Records(); len(written) != 2 {
		t.Errorf("the zone holds %v, want the validation record beside the one pointing at the edge", written)
	}
}

func TestAddHostnameDiscardsTheCertificateItSupersedes(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	stale := edge.Record{Name: "_stale.app.acme.com", Type: edge.RecordTypeCNAME, Value: "_stale.validations.invalid"}
	seedStack(t, provider, providerkit.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "shop", Class: providerkit.ClassProduction, Endpoint: "https://shop.fake.invalid"},
		Hosts: map[string]providerkit.Settled{
			"app.acme.com": {Certificate: providerkit.Certificate{ARN: "superseded", Requested: true, Written: []edge.Record{stale}}},
		},
	})
	writer, err := provider.DNS().Open(fake.KindZone, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.EnsureRecords(context.Background(), []edge.Record{stale}, nil); err != nil {
		t.Fatal(err)
	}
	provider.IssueCertificates(validationRecord)

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v %q, want the hostname settled", err, result.GetError())
	}

	if discarded := provider.Discarded(); !slices.Contains(discarded, "superseded") {
		t.Errorf("the provider discarded %v, want the superseded certificate among them", discarded)
	}
	if held := writer.(*fake.DNSWriter).Records(); slices.Contains(held, stale) {
		t.Errorf("the zone still holds %v, want the superseded validation record released", held)
	}
}

func TestAddHostnameRebindsAServedHostnameWhoseCertificateChanged(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	seedStack(t, provider, providerkit.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{
			Slug:     "shop",
			Class:    providerkit.ClassProduction,
			Endpoint: "https://shop.fake.invalid",
			Front:    "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]providerkit.Settled{
			"app.acme.com": {
				Certificate: providerkit.Certificate{ARN: "cert-of-yesterday"},
				Probe:       providerkit.Probe{OK: true, Edge: fake.KindRelay},
			},
		},
	})
	provider.Pin("app.acme.com", "cert-of-today")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
		Edge:       zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %v %q, want the hostname settled again", err, result.GetError())
	}

	bindings := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings()
	if len(bindings) == 0 || bindings[len(bindings)-1].Certificate != "cert-of-today" {
		t.Errorf("the edge was bound with %+v, want the hostname rebound with the certificate it is served with now", bindings)
	}
}

func TestHostnameStatusReportsWhatTheProviderSaysOfTheCertificate(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	seedStack(t, provider, providerkit.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{
			Slug:     "shop",
			Class:    providerkit.ClassProduction,
			Endpoint: "https://shop.fake.invalid",
			Front:    "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]providerkit.Settled{
			"app.acme.com": {
				Certificate: providerkit.Certificate{ARN: "pending-cert", Requested: true},
				Probe:       providerkit.Probe{OK: true, Edge: fake.KindRelay},
			},
		},
	})
	expiry := time.Now().Add(24 * time.Hour)
	provider.ReportCertificate(providerkit.CertificateHealth{
		Terminates:   true,
		Status:       "PENDING_VALIDATION",
		Domains:      []string{"other.acme.com"},
		Renewal:      "PENDING_AUTO_RENEWAL",
		ExpiresAt:    expiry.Unix(),
		ExpiringSoon: true,
	})

	status, err := client.GetHostnameStatus(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"app.acme.com"},
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
