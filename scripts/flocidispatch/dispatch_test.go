//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testProject = "floci-local"
	testRegion  = "europe-west1"
)

func flociEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := os.Getenv(endpointVariable)
	if endpoint == "" {
		t.Fatalf("no floci-gcp emulator in %s: this dispatches what the emulator holds", endpointVariable)
	}
	return endpoint
}

func uniqueName(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().String()))
	return "d" + hex.EncodeToString(sum[:6])
}

type received struct {
	lock    sync.Mutex
	bodies  [][]byte
	headers []http.Header
	answer  func(n int) int
}

func (r *received) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.lock.Lock()
	r.bodies = append(r.bodies, body)
	r.headers = append(r.headers, req.Header.Clone())
	n := len(r.bodies)
	r.lock.Unlock()
	w.WriteHeader(r.answer(n))
}

func (r *received) all() [][]byte {
	r.lock.Lock()
	defer r.lock.Unlock()
	return append([][]byte(nil), r.bodies...)
}

func (r *received) sent() []http.Header {
	r.lock.Lock()
	defer r.lock.Unlock()
	return append([]http.Header(nil), r.headers...)
}

func newDispatcherFor(t *testing.T, endpoint string) *dispatcher {
	t.Helper()
	d, err := newDispatcher(context.Background(), endpoint, testProject, testRegion)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func call(t *testing.T, method, url string, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		said, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s = %d %s", method, url, resp.StatusCode, said)
	}
}

func pushSubscribed(t *testing.T, endpoint, pushTo string) (topic, subscription string) {
	t.Helper()
	name := uniqueName(t)
	topic = "projects/" + testProject + "/topics/" + name
	subscription = "projects/" + testProject + "/subscriptions/" + name
	call(t, http.MethodPut, endpoint+"/v1/"+topic, map[string]any{})
	call(t, http.MethodPut, endpoint+"/v1/"+subscription, map[string]any{
		"topic":       topic,
		"pushConfig":  map[string]any{"pushEndpoint": pushTo + "/topics/orders/consumers/ship"},
		"retryPolicy": map[string]any{"minimumBackoff": "1s", "maximumBackoff": "2s"},
	})
	t.Cleanup(func() {
		for _, path := range []string{subscription, topic} {
			req, _ := http.NewRequest(http.MethodDelete, endpoint+"/v1/"+path, nil)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	})
	return topic, subscription
}

func TestLiveAPushedMessageIsPostedAsPubSubWouldAndAcknowledgedOnceItsEndpointAnswers(t *testing.T) {
	endpoint := flociEndpoint(t)
	worker := &received{answer: func(int) int { return http.StatusNoContent }}
	server := httptest.NewServer(http.HandlerFunc(worker.serve))
	t.Cleanup(server.Close)
	topic, subscription := pushSubscribed(t, endpoint, server.URL)
	call(t, http.MethodPost, endpoint+"/v1/"+topic+":publish", map[string]any{"messages": []map[string]any{{
		"data":       base64.StdEncoding.EncodeToString([]byte(`{"n":1}`)),
		"attributes": map[string]string{"ocel-message": "01K0000000000000000000000A"},
	}}})
	d := newDispatcherFor(t, endpoint)

	if err := d.pushPulled(context.Background(), subscription); err != nil {
		t.Fatalf("pushPulled() = %v", err)
	}
	bodies := worker.all()
	if len(bodies) != 1 {
		t.Fatalf("the endpoint got %d pushes, want the one message", len(bodies))
	}
	var pushed struct {
		Message struct {
			Data        []byte            `json:"data"`
			Attributes  map[string]string `json:"attributes"`
			MessageID   string            `json:"messageId"`
			PublishTime string            `json:"publishTime"`
		} `json:"message"`
		Subscription string `json:"subscription"`
	}
	if err := json.Unmarshal(bodies[0], &pushed); err != nil {
		t.Fatalf("the push %s is not Pub/Sub's push body: %v", bodies[0], err)
	}
	if string(pushed.Message.Data) != `{"n":1}` || pushed.Message.Attributes["ocel-message"] == "" || pushed.Message.MessageID == "" ||
		pushed.Message.PublishTime == "" || pushed.Subscription != subscription {
		t.Errorf("the push carried %+v, want the message's data, attributes, id and publish time, and its subscription", pushed)
	}
	if err := d.pushPulled(context.Background(), subscription); err != nil {
		t.Fatal(err)
	}
	if got := len(worker.all()); got != 1 {
		t.Errorf("the endpoint got %d pushes after it answered the first, want the message acknowledged and gone", got)
	}
}

func TestLiveAMessageItsEndpointRefusedIsPushedAgain(t *testing.T) {
	endpoint := flociEndpoint(t)
	worker := &received{answer: func(n int) int {
		if n == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	server := httptest.NewServer(http.HandlerFunc(worker.serve))
	t.Cleanup(server.Close)
	topic, subscription := pushSubscribed(t, endpoint, server.URL)
	call(t, http.MethodPost, endpoint+"/v1/"+topic+":publish", map[string]any{"messages": []map[string]any{{"data": base64.StdEncoding.EncodeToString([]byte(`{}`))}}})
	d := newDispatcherFor(t, endpoint)

	deadline := time.Now().Add(10 * time.Second)
	for len(worker.all()) < 2 && time.Now().Before(deadline) {
		if err := d.pushPulled(context.Background(), subscription); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := len(worker.all()); got != 2 {
		t.Errorf("the endpoint got %d pushes, want the refused message pushed again", got)
	}
}

func TestLiveAMessageOfAKeyWaitsUntilTheOneBeforeItIsAnswered(t *testing.T) {
	endpoint := flociEndpoint(t)
	worker := &received{answer: func(n int) int {
		if n == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}}
	server := httptest.NewServer(http.HandlerFunc(worker.serve))
	t.Cleanup(server.Close)
	topic, subscription := pushSubscribed(t, endpoint, server.URL)
	call(t, http.MethodPost, endpoint+"/v1/"+topic+":publish", map[string]any{"messages": []map[string]any{
		{"data": base64.StdEncoding.EncodeToString([]byte(`{"n":1}`)), "orderingKey": "eu"},
		{"data": base64.StdEncoding.EncodeToString([]byte(`{"n":2}`)), "orderingKey": "eu"},
	}})
	d := newDispatcherFor(t, endpoint)

	deadline := time.Now().Add(10 * time.Second)
	for len(worker.all()) < 3 && time.Now().Before(deadline) {
		if err := d.pushPulled(context.Background(), subscription); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var order []string
	for _, body := range worker.all() {
		var pushed struct {
			Message struct {
				Data []byte `json:"data"`
			} `json:"message"`
		}
		_ = json.Unmarshal(body, &pushed)
		order = append(order, string(pushed.Message.Data))
	}
	if want := []string{`{"n":1}`, `{"n":1}`, `{"n":2}`}; !slices.Equal(order, want) {
		t.Errorf("the endpoint got %v, want the refused first message pushed again before the second of its key", order)
	}
}

func TestLiveADueHTTPTaskIsSentAndDeletedAndOneNotYetDueIsLeft(t *testing.T) {
	endpoint := flociEndpoint(t)
	target := &received{answer: func(int) int { return http.StatusOK }}
	server := httptest.NewServer(http.HandlerFunc(target.serve))
	t.Cleanup(server.Close)
	d := newDispatcherFor(t, endpoint)
	ctx := context.Background()
	queue := "projects/" + testProject + "/locations/" + testRegion + "/queues/" + uniqueName(t)
	if _, err := d.tasks.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{Parent: "projects/" + testProject + "/locations/" + testRegion, Queue: &cloudtaskspb.Queue{Name: queue}}); err != nil {
		t.Fatal(err)
	}
	task := func(name string, at time.Time) string {
		created, err := d.tasks.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{
			Name:         queue + "/tasks/" + name,
			ScheduleTime: timestamppb.New(at),
			MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{
				Url: server.URL + "/v1/publish", HttpMethod: cloudtaskspb.HttpMethod_POST,
				Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"task":"` + name + `"}`),
			}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return created.GetName()
	}
	due, later := task("due", time.Now().Add(-time.Second)), task("later", time.Now().Add(time.Hour))

	if err := d.dispatchDue(ctx); err != nil {
		t.Fatalf("dispatchDue() = %v", err)
	}
	if bodies := target.all(); len(bodies) != 1 || string(bodies[0]) != `{"task":"due"}` {
		t.Errorf("the target got %q, want the due task's body alone", bodies)
	}
	if _, err := d.tasks.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: due}); status.Code(err) != codes.NotFound {
		t.Errorf("GetTask(due) = %v, want the sent task deleted", err)
	}
	if _, err := d.tasks.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: later}); err != nil {
		t.Errorf("GetTask(later) = %v, want the task not yet due kept", err)
	}
}

func TestLiveATaskItsTargetRefusedWaitsOutItsQueuesBackoffBeforeItIsSentAgain(t *testing.T) {
	endpoint := flociEndpoint(t)
	target := &received{answer: func(n int) int {
		if n == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	server := httptest.NewServer(http.HandlerFunc(target.serve))
	t.Cleanup(server.Close)
	d := newDispatcherFor(t, endpoint)
	ctx := context.Background()
	queue := "projects/" + testProject + "/locations/" + testRegion + "/queues/" + uniqueName(t)
	if _, err := d.tasks.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{Parent: "projects/" + testProject + "/locations/" + testRegion, Queue: &cloudtaskspb.Queue{
		Name:        queue,
		RetryConfig: &cloudtaskspb.RetryConfig{MinBackoff: durationpb.New(time.Second), MaxBackoff: durationpb.New(2 * time.Second)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.tasks.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{
		Name:         queue + "/tasks/refused",
		ScheduleTime: timestamppb.New(time.Now().Add(-time.Second)),
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{
			Url: server.URL + "/v1/publish", HttpMethod: cloudtaskspb.HttpMethod_POST, Body: []byte(`{}`),
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if err := d.dispatchDue(ctx); err != nil {
			t.Fatalf("dispatchDue() = %v", err)
		}
	}
	if got := len(target.all()); got != 1 {
		t.Fatalf("the target got %d sends within the queue's backoff, want the refused task held until it passes", got)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(target.all()) < 2 && time.Now().Before(deadline) {
		if err := d.dispatchDue(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := len(target.all()); got != 2 {
		t.Errorf("the target got %d sends, want the refused task sent again once its backoff passed", got)
	}
}

func newTasksQueue(t *testing.T, d *dispatcher) string {
	t.Helper()
	parent := "projects/" + testProject + "/locations/" + testRegion
	queue := parent + "/queues/" + uniqueName(t)
	if _, err := d.tasks.CreateQueue(context.Background(), &cloudtaskspb.CreateQueueRequest{Parent: parent, Queue: &cloudtaskspb.Queue{Name: queue}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.tasks.DeleteQueue(context.Background(), &cloudtaskspb.DeleteQueueRequest{Name: queue}) })
	return queue
}

func attachTasksAPI(t *testing.T, d *dispatcher) *httptest.Server {
	t.Helper()
	key, err := newSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	api := newTasksAPI(func(ctx context.Context, request *cloudtaskspb.CreateTaskRequest) (*cloudtaskspb.Task, error) {
		return d.tasks.CreateTask(ctx, request)
	}, key)
	d.api = api
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return server
}

func postRefresh(t *testing.T, api *httptest.Server, queue, name, target string, body []byte, signature string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"task": map[string]any{
		"name": queue + "/tasks/" + name,
		"httpRequest": map[string]any{
			"url":        target,
			"httpMethod": "POST",
			"headers":    map[string]string{"Content-Type": "application/json", "x-ocel-refresh-signature": signature},
			"body":       base64.StdEncoding.EncodeToString(body),
			"oidcToken": map[string]any{
				"serviceAccountEmail": "refresh@x.iam.gserviceaccount.com",
				"audience":            "https://web-1.europe-west1.run.app/_ocel/refresh",
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(api.URL+"/v2/"+queue+"/tasks", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestLiveATaskPostedThroughTheTasksAPIIsSentWithAnIDTokenForItsAudienceAndItsSignatureIntact(t *testing.T) {
	endpoint := flociEndpoint(t)
	target := &received{answer: func(int) int { return http.StatusNoContent }}
	server := httptest.NewServer(http.HandlerFunc(target.serve))
	t.Cleanup(server.Close)
	d := newDispatcherFor(t, endpoint)
	api := attachTasksAPI(t, d)
	queue := newTasksQueue(t, d)
	body := []byte("{\"isrPrefix\":\"p\",\"refresh\":{\"url\":\"/blog\"}}")
	mac := hmac.New(sha256.New, []byte("s1"))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	if resp := postRefresh(t, api, queue, "t1", server.URL+"/_ocel/refresh", body, signature); resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(resp.Body)
		t.Fatalf("the tasks API answered %d %s", resp.StatusCode, said)
	}
	stored, err := d.tasks.GetTask(context.Background(), &cloudtaskspb.GetTaskRequest{Name: queue + "/tasks/t1", ResponseView: cloudtaskspb.Task_FULL})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("floci kept the task's OidcToken: %v; its headers: %v; its body length: %d",
		stored.GetHttpRequest().GetOidcToken() != nil, stored.GetHttpRequest().GetHeaders(), len(stored.GetHttpRequest().GetBody()))

	if err := d.dispatchDue(context.Background()); err != nil {
		t.Fatalf("dispatchDue() = %v", err)
	}

	bodies, headers := target.all(), target.sent()
	if len(bodies) != 1 || !bytes.Equal(bodies[0], body) {
		t.Fatalf("the target got %q, want the task's exact body", bodies)
	}
	if got := headers[0].Get("x-ocel-refresh-signature"); got != signature {
		t.Errorf("the signature header is %q, want %q", got, signature)
	}
	check := hmac.New(sha256.New, []byte("s1"))
	check.Write(bodies[0])
	if hex.EncodeToString(check.Sum(nil)) != headers[0].Get("x-ocel-refresh-signature") {
		t.Error("the signature does not verify against the body the target received")
	}
	token := strings.TrimPrefix(headers[0].Get("Authorization"), "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the Authorization header is %q, want a Bearer ID token", headers[0].Get("Authorization"))
	}
	var claims struct{ Aud, Email string }
	decodeSegment(t, parts[1], &claims)
	if claims.Aud != "https://web-1.europe-west1.run.app/_ocel/refresh" || claims.Email != "refresh@x.iam.gserviceaccount.com" {
		t.Errorf("the token claims %+v, want the audience and account the task named", claims)
	}
	certs, err := http.Get(api.URL + "/oauth2/v3/certs")
	if err != nil {
		t.Fatal(err)
	}
	defer certs.Body.Close()
	var set struct{ Keys []jwk }
	if err := json.NewDecoder(certs.Body).Decode(&set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("the certs answer holds %v, %v", set.Keys, err)
	}
	n, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].E)
	public := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	signed, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signed); err != nil {
		t.Errorf("the published key does not verify the token the target received: %v", err)
	}
	if _, err := d.tasks.GetTask(context.Background(), &cloudtaskspb.GetTaskRequest{Name: queue + "/tasks/t1"}); status.Code(err) != codes.NotFound {
		t.Errorf("GetTask() = %v, want the sent task deleted", err)
	}
}

func TestLiveASecondTaskWithTheSameNamePostedThroughTheTasksAPIIsAnsweredAlreadyExists(t *testing.T) {
	endpoint := flociEndpoint(t)
	d := newDispatcherFor(t, endpoint)
	api := attachTasksAPI(t, d)
	queue := newTasksQueue(t, d)
	later := "http://127.0.0.1:1/_ocel/refresh"

	first := postRefresh(t, api, queue, "same", later, []byte("{}"), "sig")
	second := postRefresh(t, api, queue, "same", later, []byte("{}"), "sig")

	if first.StatusCode != http.StatusOK || second.StatusCode != http.StatusConflict {
		t.Errorf("the creates answered %d then %d, want 200 then 409", first.StatusCode, second.StatusCode)
	}
}
