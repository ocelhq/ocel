package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/queues"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	kindQueue         = "Cloudflare::Queue"
	kindQueueConsumer = "Cloudflare::QueueConsumer"

	refreshQueueBinding = "OCEL_REFRESH_QUEUE"

	reasonRefresherCertificate = "binds the tier's new worker client certificate"
	reasonConsumerSettings     = "its settings differ from this build's"
	reasonForeignConsumer      = "another consumer drains the queue; it is replaced"
)

type refreshConsumerSettings struct {
	BatchSize      int `json:"batch_size"`
	MaxRetries     int `json:"max_retries"`
	MaxWaitTimeMs  int `json:"max_wait_time_ms"`
	MaxConcurrency int `json:"max_concurrency"`
}

var wantedRefreshConsumer = refreshConsumerSettings{BatchSize: 10, MaxRetries: 4, MaxWaitTimeMs: 1000, MaxConcurrency: 10}

func refreshQueueNameFor(namespace string, tier environment.Tier) (string, error) {
	return accountNameFor("refresh queue", namespace, tier, "refresh")
}

func refresherScriptNameFor(namespace string, tier environment.Tier) (string, error) {
	return accountNameFor("refresher worker", namespace, tier, "refresher")
}

func refresherWorker(certificateID string) edge.Worker {
	worker := edge.Worker{Main: workerModule(refresherBundle)}
	if certificateID != "" {
		worker.ClientCertificates = map[string]string{edge.OriginClientCertificateBinding: certificateID}
	}
	return worker
}

type refreshQueueState struct {
	enabled         bool
	name            string
	queueID         string
	queuePresent    bool
	consumerID      string
	consumerCurrent bool
	otherConsumers  []string
	certificateID   string
	refresher       workerState
}

func (s refreshQueueState) consumerPresent() bool { return s.consumerID != "" }

func (s refreshQueueState) consumerName() string { return s.name + "/" + s.refresher.scriptName }

func (p *cloudflare) readRefreshQueue(ctx context.Context, accountID string, tier environment.Tier, certificateID string) (refreshQueueState, error) {
	name, err := refreshQueueNameFor(p.namespace, tier)
	if err != nil {
		return refreshQueueState{}, err
	}
	script, err := refresherScriptNameFor(p.namespace, tier)
	if err != nil {
		return refreshQueueState{}, err
	}
	refresher := bootstrapWorker{scriptName: script, worker: refresherWorker(certificateID), what: "refresher worker"}
	state := refreshQueueState{enabled: true, name: name, certificateID: certificateID}
	if state.refresher, err = p.readWorkerState(ctx, accountID, refresher); err != nil {
		return refreshQueueState{}, fmt.Errorf("read the refresher worker: %w", err)
	}
	queue, found, err := p.findQueue(ctx, accountID, name)
	if err != nil || !found {
		return state, err
	}
	state.queuePresent, state.queueID = true, queue.QueueID
	for _, consumer := range queue.Consumers {
		if string(consumer.Type) != string(queues.ConsumerTypeWorker) || consumer.Script != script {
			state.otherConsumers = append(state.otherConsumers, consumer.ConsumerID)
			continue
		}
		state.consumerID = consumer.ConsumerID
		state.consumerCurrent = decodeConsumerSettings(consumer.Settings) == wantedRefreshConsumer
	}
	return state, nil
}

func decodeConsumerSettings(raw any) refreshConsumerSettings {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return refreshConsumerSettings{}
	}
	var settings refreshConsumerSettings
	if err := json.Unmarshal(encoded, &settings); err != nil {
		return refreshConsumerSettings{}
	}
	return settings
}

func (p *cloudflare) findQueue(ctx context.Context, accountID, name string) (queues.Queue, bool, error) {
	listed := p.client.Queues.ListAutoPaging(ctx, queues.QueueListParams{AccountID: cf.F(accountID)}, option.WithQuery("name", name))
	for listed.Next() {
		if queue := listed.Current(); queue.QueueName == name {
			return queue, true, nil
		}
	}
	if err := listed.Err(); err != nil {
		return queues.Queue{}, false, fmt.Errorf("list the account's queues: %w", err)
	}
	return queues.Queue{}, false, nil
}

func (s refreshQueueState) refresherChange() edge.PlanChange {
	name := s.refresher.scriptName
	switch {
	case !s.refresher.present:
		return edge.PlanChange{Kind: kindWorker, Name: name, Action: edge.PlanCreate}
	case !s.refresher.upToDate():
		return edge.PlanChange{Kind: kindWorker, Name: name, Action: edge.PlanUpdate, Reason: s.refresher.scriptReason()}
	case s.certificateID == "" || s.refresher.certificateID != s.certificateID:
		return edge.PlanChange{Kind: kindWorker, Name: name, Action: edge.PlanUpdate, Reason: reasonRefresherCertificate}
	}
	return edge.PlanChange{Kind: kindWorker, Name: name, Action: edge.PlanKeep, Reason: reasonCurrent}
}

func (s refreshQueueState) changes() []edge.PlanChange {
	if !s.enabled {
		return nil
	}
	changes := []edge.PlanChange{
		{Kind: kindQueue, Name: s.name, Action: presence(s.queuePresent), Reason: keptReason(s.queuePresent)},
		s.refresherChange(),
	}
	consumer := edge.PlanChange{Kind: kindQueueConsumer, Name: s.consumerName(), Action: presence(s.consumerPresent()), Reason: keptReason(s.consumerPresent())}
	switch {
	case s.consumerPresent() && !s.consumerCurrent:
		consumer.Action, consumer.Reason = edge.PlanUpdate, reasonConsumerSettings
	case len(s.otherConsumers) > 0:
		consumer.Reason = reasonForeignConsumer
		if !s.consumerPresent() {
			consumer.Action = edge.PlanCreate
		} else {
			consumer.Action = edge.PlanUpdate
		}
	}
	return append(changes, consumer)
}

func (s refreshQueueState) removals() []edge.PlanChange {
	if !s.enabled {
		return nil
	}
	var changes []edge.PlanChange
	if s.consumerPresent() {
		changes = append(changes, edge.PlanChange{Kind: kindQueueConsumer, Name: s.consumerName(), Action: edge.PlanDelete})
	}
	if s.queuePresent {
		changes = append(changes, edge.PlanChange{Kind: kindQueue, Name: s.name, Action: edge.PlanDelete})
	}
	return append(changes, s.refresher.removals()...)
}

func (p *cloudflare) ensureRefreshQueue(ctx context.Context, accountID string, state refreshQueueState, certificateID string) error {
	queueID := state.queueID
	if !state.queuePresent {
		created, err := p.client.Queues.New(ctx, queues.QueueNewParams{AccountID: cf.F(accountID), QueueName: cf.F(state.name)})
		if err != nil {
			return fmt.Errorf("create the refresh queue %q: %w", state.name, err)
		}
		queueID = created.QueueID
	}

	refresher := state.refresher
	put := !refresher.upToDate() || refresher.certificateID != certificateID
	up := upload{accountID: accountID, scriptName: refresher.scriptName, worker: refresherWorker(certificateID)}
	if put {
		if err := p.putScript(ctx, up, ""); err != nil {
			return fmt.Errorf("put the refresher worker: %w", err)
		}
	}
	if put || refresher.subdomainOn {
		if _, err := p.setSubdomain(ctx, up, false); err != nil {
			return fmt.Errorf("turn off the workers.dev subdomain of the refresher worker: %w", err)
		}
	}

	for _, other := range state.otherConsumers {
		if err := p.deleteConsumer(ctx, accountID, queueID, other); err != nil {
			return err
		}
	}
	switch {
	case !state.consumerPresent():
		_, err := p.client.Queues.Consumers.New(ctx, queueID, queues.ConsumerNewParams{
			AccountID: cf.F(accountID),
			Body: queues.ConsumerNewParamsBodyMqWorkerConsumer{
				ScriptName: cf.F(refresher.scriptName),
				Type:       cf.F(queues.ConsumerNewParamsBodyMqWorkerConsumerTypeWorker),
				Settings:   cf.F(newConsumerSettings()),
			},
		})
		if err != nil {
			return fmt.Errorf("attach the refresher worker to the refresh queue: %w", err)
		}
	case !state.consumerCurrent:
		_, err := p.client.Queues.Consumers.Update(ctx, queueID, state.consumerID, queues.ConsumerUpdateParams{
			AccountID: cf.F(accountID),
			Body: queues.ConsumerUpdateParamsBodyMqWorkerConsumer{
				ScriptName: cf.F(refresher.scriptName),
				Type:       cf.F(queues.ConsumerUpdateParamsBodyMqWorkerConsumerTypeWorker),
				Settings:   cf.F(updateConsumerSettings()),
			},
		})
		if err != nil {
			return fmt.Errorf("restore the refresh queue consumer's settings: %w", err)
		}
	}
	return nil
}

func newConsumerSettings() queues.ConsumerNewParamsBodyMqWorkerConsumerSettings {
	return queues.ConsumerNewParamsBodyMqWorkerConsumerSettings{
		BatchSize:      cf.F(float64(wantedRefreshConsumer.BatchSize)),
		MaxRetries:     cf.F(float64(wantedRefreshConsumer.MaxRetries)),
		MaxWaitTimeMs:  cf.F(float64(wantedRefreshConsumer.MaxWaitTimeMs)),
		MaxConcurrency: cf.F(float64(wantedRefreshConsumer.MaxConcurrency)),
	}
}

func updateConsumerSettings() queues.ConsumerUpdateParamsBodyMqWorkerConsumerSettings {
	return queues.ConsumerUpdateParamsBodyMqWorkerConsumerSettings{
		BatchSize:      cf.F(float64(wantedRefreshConsumer.BatchSize)),
		MaxRetries:     cf.F(float64(wantedRefreshConsumer.MaxRetries)),
		MaxWaitTimeMs:  cf.F(float64(wantedRefreshConsumer.MaxWaitTimeMs)),
		MaxConcurrency: cf.F(float64(wantedRefreshConsumer.MaxConcurrency)),
	}
}

func (p *cloudflare) deleteConsumer(ctx context.Context, accountID, queueID, consumerID string) error {
	_, err := p.client.Queues.Consumers.Delete(ctx, queueID, consumerID, queues.ConsumerDeleteParams{AccountID: cf.F(accountID)})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return fmt.Errorf("remove consumer %s from the refresh queue: %w", consumerID, err)
	}
	return nil
}

func (p *cloudflare) tearDownRefreshQueue(ctx context.Context, accountID string, tier environment.Tier) error {
	name, err := refreshQueueNameFor(p.namespace, tier)
	if err != nil {
		return err
	}
	script, err := refresherScriptNameFor(p.namespace, tier)
	if err != nil {
		return err
	}
	var errs []error
	queue, found, err := p.findQueue(ctx, accountID, name)
	if err != nil {
		return err
	}
	if found {
		var drained error
		for _, consumer := range queue.Consumers {
			drained = errors.Join(drained, p.deleteConsumer(ctx, accountID, queue.QueueID, consumer.ConsumerID))
		}
		errs = append(errs, drained)
		if drained == nil {
			_, err := p.client.Queues.Delete(ctx, queue.QueueID, queues.QueueDeleteParams{AccountID: cf.F(accountID)})
			if err != nil && !hasStatus(err, http.StatusNotFound) {
				errs = append(errs, fmt.Errorf("delete the refresh queue %q: %w", name, err))
			}
		}
	}
	if err := p.deleteScript(ctx, accountID, script); err != nil {
		errs = append(errs, fmt.Errorf("delete worker %q: %w", script, err))
	}
	return errors.Join(errs...)
}
