package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	pubsub "cloud.google.com/go/pubsub/v2/apiv1"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	endpointVariable = "OCEL_FLOCI_GCP_ENDPOINT"

	pullBatch      = 10
	pollInterval   = 200 * time.Millisecond
	listInterval   = time.Second
	pushTimeout    = 600 * time.Second
	defaultBackoff = time.Second

	taskMinBackoff   = 100 * time.Millisecond
	taskMaxBackoff   = time.Hour
	taskMaxDoublings = 16
	flociHostSuffix  = ".floci.io"
)

type dispatcher struct {
	endpoint      string
	project       string
	region        string
	http          *http.Client
	tasks         *cloudtasks.Client
	subscriptions *pubsub.SubscriptionAdminClient

	lock sync.Mutex
	held map[string]*heldMessages

	refused map[string]*refusedTask
}

type refusedTask struct {
	attempts int
	retryAt  time.Time
}

type heldMessage struct {
	ackID    string
	message  *pubsubpb.PubsubMessage
	attempts int
	retryAt  time.Time
}

type heldMessages struct {
	lock     sync.Mutex
	messages []*heldMessage
}

func emulatorOptions(endpoint string) []option.ClientOption {
	authority := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
	return []option.ClientOption{
		option.WithEndpoint(strings.TrimSuffix(authority, "/")),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

func newDispatcher(ctx context.Context, endpoint, project, region string) (*dispatcher, error) {
	tasks, err := cloudtasks.NewClient(ctx, emulatorOptions(endpoint)...)
	if err != nil {
		return nil, err
	}
	subscriptions, err := pubsub.NewSubscriptionAdminClient(ctx, emulatorOptions(endpoint)...)
	if err != nil {
		return nil, err
	}
	return &dispatcher{
		endpoint:      strings.TrimSuffix(endpoint, "/"),
		project:       project,
		region:        region,
		http:          &http.Client{Timeout: pushTimeout},
		tasks:         tasks,
		subscriptions: subscriptions,
		held:          map[string]*heldMessages{},
		refused:       map[string]*refusedTask{},
	}, nil
}

func (d *dispatcher) pushSubscriptions(ctx context.Context) ([]*pubsubpb.Subscription, error) {
	listed := d.subscriptions.ListSubscriptions(ctx, &pubsubpb.ListSubscriptionsRequest{Project: "projects/" + d.project})
	var pushed []*pubsubpb.Subscription
	for {
		each, err := listed.Next()
		if errors.Is(err, iterator.Done) {
			return pushed, nil
		}
		if err != nil {
			return nil, err
		}
		if each.GetPushConfig().GetPushEndpoint() != "" {
			pushed = append(pushed, each)
		}
	}
}

func (d *dispatcher) heldFor(name string) *heldMessages {
	d.lock.Lock()
	defer d.lock.Unlock()
	held, found := d.held[name]
	if !found {
		held = &heldMessages{}
		d.held[name] = held
	}
	return held
}

func (d *dispatcher) pushPulled(ctx context.Context, name string) error {
	subscription, err := d.subscriptions.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: name})
	if err != nil {
		return err
	}
	pulled, err := d.subscriptions.Pull(ctx, &pubsubpb.PullRequest{Subscription: name, MaxMessages: pullBatch})
	if err != nil {
		return err
	}
	held := d.heldFor(name)
	held.take(pulled.GetReceivedMessages())
	return d.pushDue(ctx, subscription, held)
}

func (h *heldMessages) take(received []*pubsubpb.ReceivedMessage) {
	h.lock.Lock()
	defer h.lock.Unlock()
	for _, each := range received {
		id := each.GetMessage().GetMessageId()
		replaced := false
		for _, kept := range h.messages {
			if kept.message.GetMessageId() == id {
				kept.ackID, replaced = each.GetAckId(), true
			}
		}
		if !replaced {
			h.messages = append(h.messages, &heldMessage{ackID: each.GetAckId(), message: each.GetMessage()})
		}
	}
}

func (h *heldMessages) due(now time.Time) [][]*heldMessage {
	h.lock.Lock()
	defer h.lock.Unlock()
	var runs [][]*heldMessage
	byKey := map[string]int{}
	blocked := map[string]bool{}
	for _, each := range h.messages {
		key := each.message.GetOrderingKey()
		if key != "" && blocked[key] {
			continue
		}
		if each.retryAt.After(now) {
			if key != "" {
				blocked[key] = true
			}
			continue
		}
		if key == "" {
			runs = append(runs, []*heldMessage{each})
			continue
		}
		if at, found := byKey[key]; found {
			runs[at] = append(runs[at], each)
			continue
		}
		byKey[key] = len(runs)
		runs = append(runs, []*heldMessage{each})
	}
	return runs
}

func (h *heldMessages) drop(done *heldMessage) {
	h.lock.Lock()
	defer h.lock.Unlock()
	for at, each := range h.messages {
		if each == done {
			h.messages = append(h.messages[:at], h.messages[at+1:]...)
			return
		}
	}
}

func (d *dispatcher) pushDue(ctx context.Context, subscription *pubsubpb.Subscription, held *heldMessages) error {
	var wg sync.WaitGroup
	runs := held.due(time.Now())
	errs := make(chan error, len(runs))
	for _, run := range runs {
		wg.Go(func() {
			for _, each := range run {
				acked, err := d.push(ctx, subscription, each)
				if err != nil || !acked {
					errs <- err
					return
				}
				held.drop(each)
			}
		})
	}
	wg.Wait()
	close(errs)
	var joined []error
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (d *dispatcher) push(ctx context.Context, subscription *pubsubpb.Subscription, held *heldMessage) (bool, error) {
	message := held.message
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"data":        message.GetData(),
			"attributes":  message.GetAttributes(),
			"messageId":   message.GetMessageId(),
			"publishTime": message.GetPublishTime().AsTime().UTC().Format(time.RFC3339Nano),
			"orderingKey": message.GetOrderingKey(),
		},
		"subscription": subscription.GetName(),
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.reachable(subscription.GetPushConfig().GetPushEndpoint()), bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if err != nil || resp.StatusCode/100 != 2 {
		held.attempts++
		held.retryAt = time.Now().Add(backoff(subscription.GetRetryPolicy(), held.attempts))
		return false, nil
	}
	return true, d.subscriptions.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{Subscription: subscription.GetName(), AckIds: []string{held.ackID}})
}

func backoff(policy *pubsubpb.RetryPolicy, attempt int) time.Duration {
	if policy == nil {
		return defaultBackoff
	}
	least, most := policy.GetMinimumBackoff().AsDuration(), policy.GetMaximumBackoff().AsDuration()
	if most <= 0 {
		most = pushTimeout
	}
	wait := least
	for doubled := 1; doubled < attempt && wait < most; doubled++ {
		wait *= 2
	}
	return min(wait, most)
}

func (d *dispatcher) reachable(endpoint string) string {
	at, err := url.Parse(endpoint)
	if err != nil || !strings.HasSuffix(at.Hostname(), flociHostSuffix) {
		return endpoint
	}
	emulator, err := url.Parse(d.endpoint)
	if err != nil {
		return endpoint
	}
	at.Host = net.JoinHostPort(at.Hostname(), emulator.Port())
	return at.String()
}

func (d *dispatcher) dispatchDue(ctx context.Context) error {
	queues := d.tasks.ListQueues(ctx, &cloudtaskspb.ListQueuesRequest{Parent: "projects/" + d.project + "/locations/" + d.region})
	var joined []error
	for {
		queue, err := queues.Next()
		if errors.Is(err, iterator.Done) {
			return errors.Join(joined...)
		}
		if err != nil {
			return errors.Join(append(joined, err)...)
		}
		joined = append(joined, d.dispatchQueue(ctx, queue))
	}
}

func (d *dispatcher) dispatchQueue(ctx context.Context, queue *cloudtaskspb.Queue) error {
	tasks := d.tasks.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: queue.GetName(), ResponseView: cloudtaskspb.Task_FULL})
	now := time.Now()
	listed := map[string]bool{}
	var joined []error
	for {
		task, err := tasks.Next()
		if errors.Is(err, iterator.Done) {
			d.forgetRefused(queue.GetName(), listed)
			return errors.Join(joined...)
		}
		if err != nil {
			return errors.Join(append(joined, err)...)
		}
		listed[task.GetName()] = true
		if task.GetScheduleTime().AsTime().After(now) || task.GetHttpRequest() == nil {
			continue
		}
		if refused, found := d.refused[task.GetName()]; found && refused.retryAt.After(now) {
			continue
		}
		joined = append(joined, d.dispatchTask(ctx, queue, task))
	}
}

func (d *dispatcher) forgetRefused(queue string, listed map[string]bool) {
	for name := range d.refused {
		if strings.HasPrefix(name, queue+"/tasks/") && !listed[name] {
			delete(d.refused, name)
		}
	}
}

func (d *dispatcher) refuse(queue *cloudtaskspb.Queue, task *cloudtaskspb.Task) {
	refused, found := d.refused[task.GetName()]
	if !found {
		refused = &refusedTask{}
		d.refused[task.GetName()] = refused
	}
	refused.attempts++
	refused.retryAt = time.Now().Add(taskBackoff(queue.GetRetryConfig(), refused.attempts))
}

func taskBackoff(config *cloudtaskspb.RetryConfig, attempt int) time.Duration {
	least, most, doublings := taskMinBackoff, taskMaxBackoff, int32(taskMaxDoublings)
	if config != nil {
		if config.GetMinBackoff() != nil {
			least = config.GetMinBackoff().AsDuration()
		}
		if config.GetMaxBackoff() != nil {
			most = config.GetMaxBackoff().AsDuration()
		}
		if config.GetMaxDoublings() > 0 {
			doublings = config.GetMaxDoublings()
		}
	}
	wait := least
	for step := 1; step < attempt && wait < most; step++ {
		if int32(step) <= doublings {
			wait *= 2
		} else {
			wait += least << doublings
		}
	}
	return min(wait, most)
}

func (d *dispatcher) dispatchTask(ctx context.Context, queue *cloudtaskspb.Queue, task *cloudtaskspb.Task) error {
	sent := task.GetHttpRequest()
	req, err := http.NewRequestWithContext(ctx, sent.GetHttpMethod().String(), sent.GetUrl(), bytes.NewReader(sent.GetBody()))
	if err != nil {
		return err
	}
	for name, value := range sent.GetHeaders() {
		req.Header.Set(name, value)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		d.refuse(queue, task)
		return nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		d.refuse(queue, task)
		return nil
	}
	if err := d.tasks.DeleteTask(ctx, &cloudtaskspb.DeleteTaskRequest{Name: task.GetName()}); err != nil && status.Code(err) != codes.NotFound {
		return err
	}
	delete(d.refused, task.GetName())
	return nil
}

func (d *dispatcher) run(ctx context.Context) {
	following := map[string]context.CancelFunc{}
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	listed := time.Time{}
	for {
		if time.Since(listed) >= listInterval {
			listed = time.Now()
			d.follow(ctx, following)
		}
		if err := d.dispatchDue(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "flocidispatch:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (d *dispatcher) follow(ctx context.Context, following map[string]context.CancelFunc) {
	pushed, err := d.pushSubscriptions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "flocidispatch:", err)
		}
		return
	}
	current := map[string]bool{}
	for _, each := range pushed {
		current[each.GetName()] = true
		if _, followed := following[each.GetName()]; followed {
			continue
		}
		followCtx, stop := context.WithCancel(ctx)
		following[each.GetName()] = stop
		go d.pushLoop(followCtx, each.GetName())
	}
	for name, stop := range following {
		if !current[name] {
			stop()
			delete(following, name)
			d.lock.Lock()
			delete(d.held, name)
			d.lock.Unlock()
		}
	}
}

func (d *dispatcher) pushLoop(ctx context.Context, name string) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		if err := d.pushPulled(ctx, name); err != nil && ctx.Err() == nil && status.Code(err) != codes.NotFound {
			fmt.Fprintln(os.Stderr, "flocidispatch:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func main() {
	os.Exit(serve())
}

func serve() int {
	endpoint := flag.String("endpoint", os.Getenv(endpointVariable), "the floci-gcp emulator to dispatch for")
	project := flag.String("project", "floci-local", "the project whose subscriptions and queues are dispatched")
	region := flag.String("region", "europe-west1", "the region whose queues are dispatched")
	flag.Parse()
	if *endpoint == "" {
		fmt.Fprintf(os.Stderr, "flocidispatch: name the emulator with -endpoint or %s\n", endpointVariable)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d, err := newDispatcher(ctx, *endpoint, *project, *region)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flocidispatch:", err)
		return 1
	}
	d.run(ctx)
	return 0
}
