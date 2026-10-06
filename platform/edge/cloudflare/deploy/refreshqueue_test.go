package cloudflare

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	refreshQueueName  = "ocel-refresh"
	refresherScript   = "ocel-refresher"
	previewRefresher  = "ocel-refresher-preview"
	previewQueueName  = "ocel-refresh-preview"
	wantedRetriesJSON = float64(4)
)

func bootstrappedWithRefresher(t *testing.T) (*cloudflare, *cfMock, edge.BootstrapOutput) {
	t.Helper()
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	out, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return p, m, out
}

func refreshPlanRows(changes []edge.PlanChange) []edge.PlanChange {
	var rows []edge.PlanChange
	for _, change := range changes {
		switch {
		case change.Kind == kindQueue, change.Kind == kindQueueConsumer, change.Kind == kindWorker && change.Name == refresherScript:
			rows = append(rows, change)
		}
	}
	return rows
}

func TestABootstrapKeepsARefreshQueueConsumedOnlyByItsRefresher(t *testing.T) {
	_, m, _ := bootstrappedWithRefresher(t)

	if !slices.Equal(m.createdQueues, []string{refreshQueueName}) {
		t.Errorf("created queues = %v, want %q", m.createdQueues, refreshQueueName)
	}
	if len(m.createdConsumers) != 1 {
		t.Fatalf("created consumers = %v, want the refresher alone", m.createdConsumers)
	}
	consumer := m.createdConsumers[0]
	if consumer["script_name"] != refresherScript || consumer["type"] != "worker" {
		t.Errorf("consumer = %v, want a worker consumer of %q", consumer, refresherScript)
	}
	want := map[string]any{"batch_size": float64(10), "max_retries": wantedRetriesJSON, "max_wait_time_ms": float64(1000), "max_concurrency": float64(10)}
	if !reflect.DeepEqual(consumer["settings"], want) {
		t.Errorf("consumer settings = %v, want %v", consumer["settings"], want)
	}
	if _, retried := consumer["settings"].(map[string]any)["retry_delay"]; retried {
		t.Error("the consumer sets a retry delay, and the refresher sets each delay itself")
	}
	if _, deadLettered := consumer["dead_letter_queue"]; deadLettered {
		t.Error("the consumer names a dead-letter queue, and nothing would drain it")
	}
	if on := m.subdomainOn[refresherScript]; on {
		t.Error("the refresher has a workers.dev subdomain, and a consumer serves no HTTP")
	}
	if !slices.ContainsFunc(m.subdomainCalls, func(call subdomainCall) bool { return call.script == refresherScript && !call.enabled }) {
		t.Errorf("subdomain calls = %v, want the refresher's switched off", m.subdomainCalls)
	}
}

func TestTheRefresherPresentsTheTiersWorkerClientCertificate(t *testing.T) {
	_, m, out := bootstrappedWithRefresher(t)

	certificate := workerCertificateOffer(t, out).Values[edge.OfferKeyClientCertificateID]
	bindings := uploadedMetadata(t, m, refresherScript)["bindings"]
	want := []any{map[string]any{"type": "mtls_certificate", "name": edge.OriginClientCertificateBinding, "certificate_id": certificate}}
	if !reflect.DeepEqual(bindings, want) {
		t.Errorf("bindings = %v, want only the worker client certificate %q", bindings, certificate)
	}
	if got := len(out.Offers); got != 4 {
		t.Errorf("offers = %d, want the cache store, both workers and the certificate, none for the queue", got)
	}
}

func TestARenewedWorkerClientCertificateRedeploysTheRefresher(t *testing.T) {
	p, m, first := bootstrappedWithRefresher(t)
	m.mtlsCertificates[0]["expires_on"] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	puts := countPuts(m, refresherScript)

	rows := refreshPlanRows(mustPlan(t, p))
	if want := (edge.PlanChange{Kind: kindWorker, Name: refresherScript, Action: edge.PlanUpdate, Reason: reasonRefresherCertificate}); !slices.Contains(rows, want) {
		t.Errorf("plan rows = %+v, want %+v", rows, want)
	}

	second, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("renewing Bootstrap: %v", err)
	}
	renewed := workerCertificateOffer(t, second).Values[edge.OfferKeyClientCertificateID]
	if renewed == workerCertificateOffer(t, first).Values[edge.OfferKeyClientCertificateID] {
		t.Fatal("the certificate was not renewed")
	}
	if countPuts(m, refresherScript) != puts+1 {
		t.Errorf("the refresher was uploaded %d times since the renewal, want once", countPuts(m, refresherScript)-puts)
	}
	bindings := bindingsByType(uploadedMetadata(t, m, refresherScript), "mtls_certificate")
	if len(bindings) != 1 || bindings[0]["certificate_id"] != renewed {
		t.Errorf("bindings = %v, want the renewed certificate %q", bindings, renewed)
	}
}

func TestARenewedWorkerClientCertificateIsTrustedByTheOriginBeforeTheRefresherPresentsIt(t *testing.T) {
	p, m, first := bootstrappedWithRefresher(t)
	m.mtlsCertificates[0]["expires_on"] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	puts := countPuts(m, refresherScript)
	var trusted []edge.Offer
	var refresherPutsAtTrust []int
	p.trustClientCertificate = func(_ context.Context, tier environment.Tier, offer edge.Offer) error {
		if tier != environment.TierProduction {
			t.Errorf("trust asked for tier %s, want production", tier)
		}
		trusted = append(trusted, offer)
		refresherPutsAtTrust = append(refresherPutsAtTrust, countPuts(m, refresherScript))
		return nil
	}

	second, err := p.Bootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("renewing Bootstrap: %v", err)
	}

	renewed := workerCertificateOffer(t, second)
	if renewed.Values[edge.OfferKeyClientCertificateID] == workerCertificateOffer(t, first).Values[edge.OfferKeyClientCertificateID] {
		t.Fatal("the certificate was not renewed")
	}
	if len(trusted) != 1 || !reflect.DeepEqual(trusted[0], renewed) {
		t.Fatalf("trust was asked for %+v, want the renewed certificate's offer once", trusted)
	}
	if !slices.Equal(refresherPutsAtTrust, []int{puts}) {
		t.Errorf("the refresher had been uploaded %v times when trust was asked, want %d: the origin must trust the new CA before the refresher presents the new certificate", refresherPutsAtTrust, puts)
	}
}

func TestABootstrapWhoseOriginCannotTrustTheRenewedCertificateLeavesTheRefresherOnTheOldOne(t *testing.T) {
	p, m, _ := bootstrappedWithRefresher(t)
	m.mtlsCertificates[0]["expires_on"] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	puts := countPuts(m, refresherScript)
	p.trustClientCertificate = func(context.Context, environment.Tier, edge.Offer) error { return errors.New("load balancer busy") }

	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err == nil {
		t.Fatal("Bootstrap succeeded, want the failure to trust the renewed certificate")
	}
	if got := countPuts(m, refresherScript); got != puts {
		t.Errorf("the refresher was uploaded %d times despite the failure, want it left on the old certificate", got-puts)
	}
}

func TestTheRefreshQueueIsFoundByItsNameEvenWhenTheListingIgnoresTheNameFilter(t *testing.T) {
	p, m, _ := bootstrappedWithRefresher(t)
	m.queues = append([]map[string]any{{"queue_id": "other-1", "queue_name": "someone-elses", "consumers": []any{}}}, m.queues...)
	m.ignoresQueueNameFilter = true

	queue, found, err := p.findQueue(t.Context(), "acct", refreshQueueName)

	if err != nil || !found || queue.QueueName != refreshQueueName {
		t.Errorf("findQueue = %+v, %v, %v, want the tier's own queue whatever the server did with the name filter", queue, found, err)
	}
}

func countPuts(m *cfMock, script string) int {
	n := 0
	for _, put := range m.putScripts {
		if put == script {
			n++
		}
	}
	return n
}

func mustPlan(t *testing.T, p *cloudflare) []edge.PlanChange {
	t.Helper()
	changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("planBootstrap: %v", err)
	}
	return changes
}

func TestABootstrapReplacesAConsumerThatIsNotItsRefresher(t *testing.T) {
	m := bootstrapMock(t, false)
	m.queues = []map[string]any{{
		"queue_id": "queue-held", "queue_name": refreshQueueName,
		"consumers": []any{map[string]any{"consumer_id": "foreign", "script": "someone-elses", "type": "worker", "settings": map[string]any{}}},
	}}
	p := mutualTLSEdge(t, m)

	rows := refreshPlanRows(mustPlan(t, p))
	if want := (edge.PlanChange{Kind: kindQueueConsumer, Name: refreshQueueName + "/" + refresherScript, Action: edge.PlanCreate, Reason: reasonForeignConsumer}); !slices.Contains(rows, want) {
		t.Errorf("plan rows = %+v, want %+v", rows, want)
	}
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if !slices.Equal(m.deletedConsumers, []string{"foreign"}) {
		t.Errorf("deleted consumers = %v, want the foreign one", m.deletedConsumers)
	}
	if len(m.createdQueues) != 0 || len(m.createdConsumers) != 1 {
		t.Errorf("created queues %v and consumers %v, want the held queue kept and our consumer created", m.createdQueues, m.createdConsumers)
	}
}

func TestABootstrapRestoresTheRefreshersConsumerSettings(t *testing.T) {
	p, m, _ := bootstrappedWithRefresher(t)
	consumer := m.queues[0]["consumers"].([]any)[0].(map[string]any)
	consumer["settings"].(map[string]any)["max_retries"] = float64(3)

	rows := refreshPlanRows(mustPlan(t, p))
	if want := (edge.PlanChange{Kind: kindQueueConsumer, Name: refreshQueueName + "/" + refresherScript, Action: edge.PlanUpdate, Reason: reasonConsumerSettings}); !slices.Contains(rows, want) {
		t.Errorf("plan rows = %+v, want %+v", rows, want)
	}
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(m.updatedConsumers) != 1 || m.updatedConsumers[0]["settings"].(map[string]any)["max_retries"] != wantedRetriesJSON {
		t.Errorf("updated consumers = %v, want one restoring max_retries to 4", m.updatedConsumers)
	}
}

func TestASecondBootstrapLeavesACurrentRefreshQueueAlone(t *testing.T) {
	p, m, _ := bootstrappedWithRefresher(t)

	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}

	if countPuts(m, refresherScript) != 1 {
		t.Errorf("the refresher was uploaded %d times, want once", countPuts(m, refresherScript))
	}
	if len(m.createdQueues) != 1 || len(m.createdConsumers) != 1 || len(m.updatedConsumers) != 0 || len(m.deletedConsumers) != 0 {
		t.Errorf("queues %v, consumers created %v updated %v deleted %v, want nothing written", m.createdQueues, m.createdConsumers, m.updatedConsumers, m.deletedConsumers)
	}
	for _, row := range refreshPlanRows(mustPlan(t, p)) {
		if row.Action != edge.PlanKeep || row.Reason != reasonCurrent {
			t.Errorf("plan row %+v, want it kept as already current", row)
		}
	}
}

func TestPlanningABootstrapNamesTheRefreshQueueItsRefresherAndTheirConsumer(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)

	want := []edge.PlanChange{
		{Kind: kindQueue, Name: refreshQueueName, Action: edge.PlanCreate},
		{Kind: kindWorker, Name: refresherScript, Action: edge.PlanCreate},
		{Kind: kindQueueConsumer, Name: refreshQueueName + "/" + refresherScript, Action: edge.PlanCreate},
	}
	if got := refreshPlanRows(mustPlan(t, p)); !reflect.DeepEqual(got, want) {
		t.Errorf("plan rows = %+v, want %+v", got, want)
	}

	removals, err := p.planRemoveBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshPlanRows(removals); len(got) != 0 {
		t.Errorf("removals of an empty account = %+v, want none", got)
	}

	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	removals, err = p.planRemoveBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []edge.PlanChange{
		{Kind: kindQueueConsumer, Name: refreshQueueName + "/" + refresherScript, Action: edge.PlanDelete},
		{Kind: kindQueue, Name: refreshQueueName, Action: edge.PlanDelete},
		{Kind: kindWorker, Name: refresherScript, Action: edge.PlanDelete},
	} {
		if !slices.Contains(removals, change) {
			t.Errorf("removals = %+v, want %+v", removals, change)
		}
	}
}

func TestTearingABootstrapDownDeletesItsRefreshQueueConsumerAndRefresher(t *testing.T) {
	p, m, _ := bootstrappedWithRefresher(t)
	m.queues = append(m.queues, map[string]any{"queue_id": "preview-queue", "queue_name": previewQueueName, "consumers": []any{}})

	if err := p.Teardown(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	if !slices.Equal(m.deletedConsumers, []string{"consumer-1"}) || !slices.Equal(m.deletedQueues, []string{"queue-1"}) {
		t.Errorf("deleted consumers %v and queues %v, want production's consumer and queue only", m.deletedConsumers, m.deletedQueues)
	}
	if !slices.Contains(m.deletedScripts, refresherScript) || slices.Contains(m.deletedScripts, previewRefresher) {
		t.Errorf("deleted scripts = %v, want %q and not the preview's", m.deletedScripts, refresherScript)
	}
}

func TestTheRefreshQueueAndItsRefresherCarryThePreviewSuffixOnThePreviewTier(t *testing.T) {
	queue, err := refreshQueueNameFor("ocel", environment.TierPreview)
	if err != nil || queue != previewQueueName {
		t.Errorf("preview queue = %q, %v, want %q", queue, err, previewQueueName)
	}
	script, err := refresherScriptNameFor("ocel", environment.TierPreview)
	if err != nil || script != previewRefresher {
		t.Errorf("preview refresher = %q, %v, want %q", script, err, previewRefresher)
	}
	if _, err := refresherScriptNameFor("a-namespace-long-enough-to-pass-the-limit-of-worker-names-for-sure-yes", environment.TierPreview); err == nil {
		t.Error("an over-long namespace named a refresher anyway")
	}
}

func TestAnAwsBootstrapKeepsNoRefreshQueue(t *testing.T) {
	seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
	m := bootstrapMock(t, false)
	p := m.provider(t)

	if rows := refreshPlanRows(mustPlan(t, p)); len(rows) != 0 {
		t.Errorf("plan rows = %+v, want none: only an edge that holds a worker client certificate keeps a refresher", rows)
	}
}
