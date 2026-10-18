package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func servedURLs(result *progressv1.OperationResult) []string {
	var urls []string
	for _, app := range result.GetApps() {
		urls = append(urls, app.GetUrls()...)
	}
	return urls
}

func findDeploymentURL(result *progressv1.OperationResult, app string) string {
	for _, appResult := range result.GetApps() {
		if appResult.GetApp() == app {
			return appResult.GetDeploymentUrl()
		}
	}
	return ""
}

func servedAppURLs(result *progressv1.OperationResult, app string) []string {
	for _, appResult := range result.GetApps() {
		if appResult.GetApp() == app {
			return appResult.GetUrls()
		}
	}
	return nil
}

func hostnameAdded(t *testing.T, client contractv1connect.ProviderServiceClient, hosts ...string) {
	t.Helper()
	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts(hosts...),
		Edge:       &contractv1.EdgeSelection{Dns: &contractv1.Dns{Kind: string(fake.KindZone), Zone: "shop.example"}},
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, %v", result.GetError(), err)
	}
}

func writtenBy(zone string) *contractv1.EdgeSelection {
	return &contractv1.EdgeSelection{Dns: &contractv1.Dns{Kind: string(fake.KindZone), Zone: zone}}
}

func TestTheFirstDeployAttachesAHostnameItsDNSWriterPoints(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if !slices.Equal(servedURLs(result), []string{"https://shop.example"}) || noteOf(result) != "" {
		t.Errorf("the first deploy returned urls %v and the note %q, want the hostname it attached printed: no record was left to write by hand, so nothing waits on anyone",
			servedURLs(result), noteOf(result))
	}
}

func TestTheFirstDeployAttachesALocalhostNameWithNoDNSWriter(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains[0].Hostnames = []string{"web-j-1-deploy-python.localhost"}
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if manual := manualRecordsIn(events); len(manual) != 0 {
		t.Errorf("the deploy asked for manual records %v, want none: a localhost name resolves without any record", manual)
	}
	if want := []string{"https://web-j-1-deploy-python.localhost"}; !slices.Equal(servedURLs(result), want) || noteOf(result) != "" {
		t.Errorf("the deploy printed %v with the note %q, want %v", servedURLs(result), noteOf(result), want)
	}
}

func TestADeployBindsItsHostnamesOnlyOnceEveryStackItProvisionsIsUp(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	var said []string
	bound, provisioned := -1, -1
	for _, event := range events {
		message := saidLine(event)
		switch {
		case strings.HasPrefix(message, "Binding shop.example") && bound < 0:
			bound = len(said)
		case strings.HasPrefix(message, "Provisioned stack "):
			provisioned = len(said)
		}
		if message != "" {
			said = append(said, message)
		}
	}
	if bound < 0 || provisioned < 0 || bound < provisioned {
		t.Errorf("the deploy said %v, want shop.example bound only after every stack was provisioned: an edge binds what already exists, such as the box's store name", said)
	}
}

func TestADeployDeclaringAWildcardForProductionIsRefusedBeforeItProvisionsAnything(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains[0].Hostnames = []string{"*.shop.example"}
	_, _, err := deployStream(t, client, req)
	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid || !strings.Contains(err.Error(), "domains.preview") {
		t.Fatalf("Deploy() = %v, want it refused as invalid: a wildcard belongs to domains.preview", err)
	}
	if specs := p.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused deploy provisioned %d stacks, want none: the hostname is read before anything is built", len(specs))
	}
}

func TestADeployLeavesAHostnameAnotherEdgeServesToDomainAdd(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindRelay, result.GetError())
	}

	req.Edge.Kind = string(fake.KindDirect)
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindDirect, result.GetError())
	}
	note := noteOf(result)
	if !strings.Contains(note, "shop.example") || !strings.Contains(note, string(fake.KindRelay)) || !strings.Contains(note, "`ocel domain add`") {
		t.Errorf("the note = %q, want it naming the hostname, the edge that still serves it, and that `ocel domain add` moves it", note)
	}
	edges := vendor.Edges().(*fake.Edges)
	if bound := edges.Edge(fake.KindDirect).Bindings(); len(bound) != 0 {
		t.Errorf("the %s edge binds %v, want nothing: moving a hostname between edges is `ocel domain add`'s to order", fake.KindDirect, bound)
	}
	if bound := edges.Edge(fake.KindRelay).Bindings(); len(bound) != 1 || bound[0].Hostname != "shop.example" {
		t.Errorf("the %s edge binds %v, want shop.example still bound there: nothing moved it off", fake.KindRelay, bound)
	}
}

func TestALaterDeployAttachesAHostnameTheConfigNewlyDeclares(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	req.Manifest.Domains[0].Hostnames = append(req.Manifest.Domains[0].Hostnames, "www.shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("a deploy declaring one more hostname = %q, want it attached: the config is the declaration and the deploy reconciles it", result.GetError())
	}
	if want := []string{"https://shop.example", "https://www.shop.example"}; !slices.Equal(servedURLs(result), want) {
		t.Errorf("the deploy printed %v, want %v", servedURLs(result), want)
	}
}

func TestADeployWhoseDNSWriterFailsPromotesNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	writer, err := vendor.DNS().Open(fake.KindZone, "shop.example", "")
	if err != nil {
		t.Fatal(err)
	}
	writer.(*fake.DNSRecords).Refuse(errors.New("the zone's api answered 500"))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if result.GetSuccess() {
		t.Fatalf("Deploy() succeeded, want it failed: the dns writer broke, which no one waiting fixes")
	}
	if _, promoted := spanStatuses(events)[promotionSpan]; promoted {
		t.Error("the run promoted, want nothing promoted once attaching a declared hostname failed")
	}
}

func TestADeployRefusedForAReasonNoWaitingFixesFailsWithThatReason(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	reason := "no load balancer exists in this project for shop.example: run `ocel bootstrap`"
	vendor.RefuseCertificates(refusal.Refuse(refusal.CodeNotReady, "%s", reason))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if result.GetSuccess() {
		t.Fatalf("Deploy() succeeded with the note %q, want it failed: a missing load balancer waits on `ocel bootstrap`, not on `ocel domain add`", noteOf(result))
	}
	if !strings.Contains(result.GetError(), reason) {
		t.Errorf("Deploy() = %q, want the refusal's own remedy", result.GetError())
	}
	if _, promoted := spanStatuses(events)[promotionSpan]; promoted {
		t.Error("the run promoted, want nothing promoted over a hostname that could not be attached")
	}
}

func TestADeployWhoseCertificateIsStillIssuingLeavesItToDomainAdd(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)
	p.RequireValidationRecords(edge.Record{Name: "_acme.shop.example", Type: edge.RecordTypeCNAME, Value: "validate.example"})
	p.StallAfterProving(provider.Resumable(refusal.Refuse(refusal.CodeNotReady, "the certificate is still validating")))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: an issuance still in flight delays the hostname, not the release", result.GetError())
	}
	if !strings.Contains(noteOf(result), "the certificate is still validating") {
		t.Errorf("the note = %q, want it naming the issuance it waits on", noteOf(result))
	}
}

func TestADeployWhoseCertificateWaitsOnYouLeavesItToDomainAdd(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.RequireValidationRecords(edge.Record{Name: "_acme.shop.example", Type: edge.RecordTypeCNAME, Value: "validate.example"})

	result, events := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed without waiting on a record only you can write", result.GetError())
	}
	if manual := manualRecordsIn(events); !slices.Contains(manual, "_acme.shop.example CNAME") {
		t.Errorf("the deploy asked for manual records %v, want the record that proves the certificate", manual)
	}
	if len(servedURLs(result)) != 0 || !strings.Contains(noteOf(result), "Prove you own shop.example") {
		t.Errorf("the deploy printed %v with the note %q, want no url and a note naming the proof it waits on",
			servedURLs(result), noteOf(result))
	}
}

func manualRecordsIn(events []*progressv1.OperationEvent) []string {
	var manual []string
	for _, event := range events {
		for _, record := range event.GetDnsManualRecords().GetRecords() {
			manual = append(manual, record.GetName()+" "+record.GetType())
		}
	}
	return manual
}

func TestDeployNeedingManualRecordsSucceedsAndLeavesTheHostnameToDomainAdd(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: a record only you can write delays the hostname, not the release", result.GetError())
	}
	if len(servedURLs(result)) != 0 {
		t.Errorf("the deploy printed %v, want no url: shop.example answers nowhere until its record is written", servedURLs(result))
	}
	if manual := manualRecordsIn(events); !slices.Contains(manual, "shop.example CNAME") {
		t.Errorf("the deploy asked for manual records %v, want the record that points shop.example at the edge, told the way domain add tells it", manual)
	}
	note := noteOf(result)
	if !strings.Contains(note, "shop.example") || !strings.Contains(note, "ocel domain add") || strings.Contains(note, "deploy again") {
		t.Errorf("the note = %q, want it naming the hostname, the record to add and that `ocel domain add` resumes it — never another deploy", note)
	}

	hostnameAdded(t, client, "shop.example")
}
