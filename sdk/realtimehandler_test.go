package ocel_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"ocel.dev"
	realtimev1 "ocel.dev/internal/proto/app/realtime/v1"
	"ocel.dev/internal/proto/app/realtime/v1/realtimev1connect"
)

type realtimeFixture struct {
	Name     string `json:"name"`
	Realtime struct {
		Transport  string `json:"transport"`
		URL        string `json:"url"`
		Host       string `json:"host"`
		SigningKey []byte `json:"signingKey"`
		VerifyKey  []byte `json:"verifyKey"`
	} `json:"realtime"`
}

func readRealtimeFixture(t *testing.T) realtimeFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "proto", "common", "bindings", "v1", "fixtures", "realtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture realtimeFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func deliverRealtime(t *testing.T, name string, fixture realtimeFixture) {
	t.Helper()
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCEL_RESOURCE_REALTIME_"+name, string(raw))
}

type tokenClaims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Ocel      struct {
		Operation string `json:"op"`
		Channel   string `json:"ch"`
		Namespace string `json:"ns"`
	} `json:"ocel"`
}

func readClaims(t *testing.T, verifyKey []byte, token string) tokenClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is no JWT", token)
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(verifyKey, []byte(parts[0]+"."+parts[1]), signature) {
		t.Fatalf("token %q does not verify against the fixture's verify key", token)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims tokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

type publishedEvent struct {
	Realtime string
	Channel  string
	Envelope struct {
		Version   int             `json:"v"`
		ID        string          `json:"id"`
		Channel   string          `json:"ch"`
		Timestamp int64           `json:"ts"`
		Kind      string          `json:"kind"`
		Data      json.RawMessage `json:"data"`
	}
}

type fakeRealtimeRuntime struct {
	t           *testing.T
	mutex       sync.Mutex
	published   []publishedEvent
	refusal     error
	delay       time.Duration
	inFlight    int
	maxInFlight int
}

func (r *fakeRealtimeRuntime) Publish(ctx context.Context, req *realtimev1.PublishRequest) (*realtimev1.PublishResponse, error) {
	event := publishedEvent{Realtime: req.GetRealtime(), Channel: req.GetChannel()}
	if err := json.Unmarshal([]byte(req.GetEvent()), &event.Envelope); err != nil {
		r.t.Errorf("the runtime received %q, which is no envelope: %v", req.GetEvent(), err)
	}
	r.mutex.Lock()
	r.inFlight++
	r.maxInFlight = max(r.maxInFlight, r.inFlight)
	delay, refusal := r.delay, r.refusal
	r.mutex.Unlock()
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.inFlight--
	if refusal != nil {
		return nil, refusal
	}
	r.published = append(r.published, event)
	return &realtimev1.PublishResponse{}, nil
}

func (r *fakeRealtimeRuntime) listEvents() []publishedEvent {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]publishedEvent(nil), r.published...)
}

func (r *fakeRealtimeRuntime) answerSlowly(delay time.Duration) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.delay = delay
}

func (r *fakeRealtimeRuntime) refuse(err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.refusal = err
}

func (r *fakeRealtimeRuntime) readMaxInFlight() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.maxInFlight
}

func serveFakeRealtimeRuntime(t *testing.T, name string) (*fakeRealtimeRuntime, realtimeFixture) {
	t.Helper()
	runtime := &fakeRealtimeRuntime{t: t}
	path, handler := realtimev1connect.NewRealtimeServiceHandler(runtime)
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+storeToken {
			http.Error(w, "this request has no valid session token", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("OCEL_RUNTIME_ADDRESS", server.URL)
	t.Setenv("OCEL_SESSION_TOKEN", storeToken)
	fixture := readRealtimeFixture(t)
	fixture.Realtime.Transport = "REALTIME_TRANSPORT_OCEL_GATEWAY"
	fixture.Realtime.Host = "realtime.shop.example"
	fixture.Realtime.URL = "wss://realtime.shop.example/event/realtime"
	deliverRealtime(t, name, fixture)
	return runtime, fixture
}

type realtimeAnswer struct {
	Transport string `json:"transport"`
	URL       string `json:"url"`
	Host      string `json:"host"`
	Connect   *struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	} `json:"connect"`
	Grants []struct {
		Index int    `json:"i"`
		Wire  string `json:"wire"`
		Token string `json:"token"`
	} `json:"grants"`
	Denied []struct {
		Index int    `json:"i"`
		Code  string `json:"code"`
	} `json:"denied"`
}

func postRealtime(handler http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "https://shop.example/api/realtime", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func readAnswer(t *testing.T, res *httptest.ResponseRecorder) realtimeAnswer {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", res.Code, res.Body)
	}
	var answer realtimeAnswer
	if err := json.Unmarshal(res.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

type handlerRules struct {
	mutex              sync.Mutex
	subscribeCalls     []*ocel.SubscribeContext[caller, orderParams]
	publishCalls       []*ocel.PublishContext[caller, roomParams, chatMessage]
	failNextSubscribe  bool
	ownedOrdersByOwner map[string]string
}

func declareHandlerResource(rules *handlerRules) *ocel.RealtimeDefinition {
	resource := ocel.Realtime("app", ocel.RealtimeAuthorize(authorizeByHeader), ocel.RealtimeTokenTTL(30*time.Second))
	ocel.Channel[orderEvent, orderParams](resource, "orders/:orderId",
		ocel.ChannelSubscribe(func(c *ocel.SubscribeContext[caller, orderParams]) (bool, error) {
			rules.mutex.Lock()
			defer rules.mutex.Unlock()
			rules.subscribeCalls = append(rules.subscribeCalls, c)
			if rules.failNextSubscribe {
				rules.failNextSubscribe = false
				return false, errors.New("db down")
			}
			return rules.ownedOrdersByOwner[c.Params.OrderID] == c.Auth.ID, nil
		}))
	ocel.Channel[orderEvent, deployParams](resource, "projects/:projectId/deploys/:deployId", ocel.ChannelWildcard(),
		ocel.ChannelSubscribe(func(c *ocel.SubscribeContext[caller, deployParams]) (bool, error) { return true, nil }))
	ocel.Channel[chatMessage, roomParams](resource, "rooms/:roomId",
		ocel.ChannelSubscribe(func(c *ocel.SubscribeContext[caller, roomParams]) (bool, error) { return true, nil }),
		ocel.ChannelPublish(func(c *ocel.PublishContext[caller, roomParams, chatMessage]) (bool, error) {
			rules.mutex.Lock()
			defer rules.mutex.Unlock()
			rules.publishCalls = append(rules.publishCalls, c)
			return c.Auth.ID == "u1", nil
		}))
	ocel.Channel[orderEvent, struct{}](resource, "status", ocel.ChannelSubscribePublic())
	return resource
}

func newHandlerRules() *handlerRules {
	return &handlerRules{ownedOrdersByOwner: map[string]string{"o-1": "u1"}}
}

func TestTheHandlerNamesTheTransportAndMintsAConnectTokenWhenAsked(t *testing.T) {
	fixture := readRealtimeFixture(t)
	deliverRealtime(t, "app", fixture)
	handler := declareHandlerResource(newHandlerRules()).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"connect":true,"ops":[]}`, nil))

	if answer.Transport != "appsync-events" || answer.URL != fixture.Realtime.URL || answer.Host != fixture.Realtime.Host {
		t.Errorf("answer = %+v, want appsync-events at the fixture's url and host", answer)
	}
	if answer.Connect == nil {
		t.Fatal("connect = nil, want a connect token")
	}
	claims := readClaims(t, fixture.Realtime.VerifyKey, answer.Connect.Token)
	if claims.Issuer != "ocel:rt:app" || claims.Audience != fixture.Realtime.Host || claims.Subject != "anonymous" ||
		claims.Ocel.Operation != "connect" || claims.Ocel.Channel != "/app" || claims.Ocel.Namespace != "app" {
		t.Errorf("claims = %+v, want a connect token for /app", claims)
	}
	if claims.ExpiresAt != answer.Connect.ExpiresAt || claims.ExpiresAt-claims.IssuedAt != 30 {
		t.Errorf("exp %d iat %d expiresAt %d, want 30s apart and expiresAt the exp", claims.ExpiresAt, claims.IssuedAt, answer.Connect.ExpiresAt)
	}
}

func TestTheHandlerAnswersASocketPathOnTheOriginTheRequestReachedItAt(t *testing.T) {
	fixture := readRealtimeFixture(t)
	fixture.Realtime.Transport = "REALTIME_TRANSPORT_OCEL_GATEWAY"
	fixture.Realtime.URL = "/.well-known/ocel-realtime"
	fixture.Realtime.Host = "gateway:8080"
	deliverRealtime(t, "app", fixture)
	handler := declareHandlerResource(newHandlerRules()).Handler()

	if answer := readAnswer(t, postRealtime(handler, `{"ops":[]}`, nil)); answer.URL != "wss://shop.example/.well-known/ocel-realtime" {
		t.Errorf("url = %q, want the socket path on the https origin the request reached", answer.URL)
	}
	forwarded := map[string]string{"X-Forwarded-Proto": "http", "X-Forwarded-Host": "web.localhost"}
	if answer := readAnswer(t, postRealtime(handler, `{"ops":[]}`, forwarded)); answer.URL != "ws://web.localhost/.well-known/ocel-realtime" {
		t.Errorf("url = %q, want the socket path on the forwarded http origin", answer.URL)
	}
}

func TestTheHandlerGrantsAPublicSubscribeToAnyone(t *testing.T) {
	fixture := readRealtimeFixture(t)
	deliverRealtime(t, "app", fixture)
	handler := declareHandlerResource(newHandlerRules()).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"ops":[{"op":"subscribe","pattern":"status","params":{}}]}`, nil))

	if answer.Connect != nil || len(answer.Denied) != 0 || len(answer.Grants) != 1 || answer.Grants[0].Wire != "/app/status" {
		t.Fatalf("answer = %+v, want one grant for /app/status", answer)
	}
	claims := readClaims(t, fixture.Realtime.VerifyKey, answer.Grants[0].Token)
	if claims.Subject != "anonymous" || claims.Ocel.Operation != "subscribe" || claims.Ocel.Channel != "/app/status" {
		t.Errorf("claims = %+v, want an anonymous subscribe to /app/status", claims)
	}
}

func TestTheHandlerRunsTheSubscribeRuleWithTheAuthParamsAndRequest(t *testing.T) {
	fixture := readRealtimeFixture(t)
	deliverRealtime(t, "app", fixture)
	rules := newHandlerRules()
	handler := declareHandlerResource(rules).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"connect":true,"ops":[
		{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":"o-1"}},
		{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":"o-2"}}]}`, map[string]string{"X-User": "u1"}))

	if len(answer.Grants) != 1 || answer.Grants[0].Index != 0 || answer.Grants[0].Wire != "/app/orders/o-1" {
		t.Errorf("grants = %+v, want o-1 granted", answer.Grants)
	}
	if len(answer.Denied) != 1 || answer.Denied[0].Index != 1 || answer.Denied[0].Code != "forbidden" {
		t.Errorf("denied = %+v, want o-2 forbidden", answer.Denied)
	}
	if sub := readClaims(t, fixture.Realtime.VerifyKey, answer.Connect.Token).Subject; sub != "u1" {
		t.Errorf("connect sub = %q, want the auth's id", sub)
	}
	if sub := readClaims(t, fixture.Realtime.VerifyKey, answer.Grants[0].Token).Subject; sub != "u1" {
		t.Errorf("subscribe sub = %q, want the auth's id", sub)
	}
	for _, call := range rules.subscribeCalls {
		if call.Auth.ID != "u1" || call.Request.Header.Get("X-User") != "u1" {
			t.Errorf("rule saw auth %+v and request header %q, want u1's", call.Auth, call.Request.Header.Get("X-User"))
		}
	}
}

func TestTheHandlerDeniesARuledOpToNobodyWithoutRunningTheRule(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	rules := newHandlerRules()
	handler := declareHandlerResource(rules).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"ops":[{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":"o-1"}}]}`, nil))

	if len(answer.Denied) != 1 || answer.Denied[0].Code != "unauthenticated" {
		t.Errorf("denied = %+v, want unauthenticated", answer.Denied)
	}
	if len(rules.subscribeCalls) != 0 {
		t.Errorf("the rule ran %d times, want never", len(rules.subscribeCalls))
	}
}

func TestTheHandlerGrantsAWildcardSubscribeAsThePrefixAndStar(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"ops":[{"op":"subscribe","pattern":"projects/:projectId/deploys/:deployId","params":{"projectId":"p_1"}}]}`, map[string]string{"X-User": "u1"}))

	if len(answer.Grants) != 1 || answer.Grants[0].Wire != "/app/projects/0zobptc/deploys/*" {
		t.Errorf("grants = %+v, want the encoded prefix and /*", answer.Grants)
	}
}

func TestTheHandlerDeniesEachOpItCannotServeWithItsCode(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"ops":[
		{"op":"subscribe","pattern":"nope","params":{}},
		{"op":"subscribe","pattern":"orders/:orderId","params":{}},
		{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":7}},
		{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}},
		{"op":"unsubscribe","pattern":"status","params":{}},
		{"op":"publish","pattern":"status","params":{},"body":{"status":"up"}}]}`, map[string]string{"X-User": "u1"}))

	want := []string{"unknown-pattern", "missing-param", "invalid-params", "value-too-long", "unknown-op", "no-publish-rule"}
	if len(answer.Grants) != 0 || len(answer.Denied) != len(want) {
		t.Fatalf("answer = %+v, want every op denied", answer)
	}
	for i, code := range want {
		if answer.Denied[i].Index != i || answer.Denied[i].Code != code {
			t.Errorf("denied[%d] = %+v, want %s", i, answer.Denied[i], code)
		}
	}
}

func TestTheHandlerDeniesAnOpWhoseRuleFailsAndServesTheRest(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	rules := newHandlerRules()
	rules.failNextSubscribe = true
	handler := declareHandlerResource(rules).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"ops":[
		{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":"o-1"}},
		{"op":"subscribe","pattern":"status","params":{}}]}`, map[string]string{"X-User": "u1"}))

	if len(answer.Denied) != 1 || answer.Denied[0].Code != "rule-error" || len(answer.Grants) != 1 || answer.Grants[0].Index != 1 {
		t.Errorf("answer = %+v, want the rule's op denied rule-error and status granted", answer)
	}
}

func TestTheHandlerAnswersUncacheableAndSetsNoCookie(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	res := postRealtime(handler, `{"connect":true,"ops":[]}`, nil)

	if res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Set-Cookie") != "" {
		t.Errorf("headers = %v, want no-store and no cookie", res.Header())
	}
}

func TestTheHandlerRefusesWhatIsNoBatchOfJSON(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "https://shop.example/api/realtime", nil))
	if get.Code != http.StatusMethodNotAllowed || get.Header().Get("Allow") != "POST" || get.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("GET = %d %v, want 405 allowing POST", get.Code, get.Header())
	}
	if res := postRealtime(handler, `{}`, map[string]string{"Content-Type": "text/plain"}); res.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain = %d, want 415", res.Code)
	}
	for _, body := range []string{`{`, `{"ops":"x"}`, `{}`} {
		if res := postRealtime(handler, body, nil); res.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, res.Code)
		}
	}
	ops := strings.Repeat(`{"op":"subscribe","pattern":"status"},`, 51)
	if res := postRealtime(handler, `{"ops":[`+strings.TrimSuffix(ops, ",")+`]}`, nil); res.Code != http.StatusBadRequest {
		t.Errorf("51 ops = %d, want 400", res.Code)
	}
}

func TestTheHandlerServesItsOwnOriginAndRefusesAnotherWithoutCORS(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	same := postRealtime(handler, `{"ops":[]}`, map[string]string{"Origin": "https://shop.example"})
	other := postRealtime(handler, `{"ops":[]}`, map[string]string{"Origin": "https://evil.example"})
	forwarded := httptest.NewRequest(http.MethodPost, "http://10.0.0.5:3000/api/realtime", strings.NewReader(`{"ops":[]}`))
	forwarded.Header.Set("Content-Type", "application/json")
	forwarded.Header.Set("Origin", "https://shop.example")
	forwarded.Header.Set("X-Forwarded-Host", "shop.example")
	forwarded.Header.Set("X-Forwarded-Proto", "HTTPS, http")
	behindProxy := httptest.NewRecorder()
	handler.ServeHTTP(behindProxy, forwarded)

	if same.Code != http.StatusOK || same.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("same origin = %d %v, want 200 without CORS", same.Code, same.Header())
	}
	if other.Code != http.StatusForbidden || other.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("other origin = %d %v, want 403 without CORS", other.Code, other.Header())
	}
	if behindProxy.Code != http.StatusOK {
		t.Errorf("behind a proxy = %d, want the forwarded host and scheme judged its own", behindProxy.Code)
	}
}

func TestTheHandlerRefusesItsOwnHostUnderAnotherScheme(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	plain := postRealtime(handler, `{"ops":[]}`, map[string]string{"Origin": "http://shop.example"})
	forwarded := httptest.NewRequest(http.MethodPost, "https://10.0.0.5:3000/api/realtime", strings.NewReader(`{"ops":[]}`))
	forwarded.Header.Set("Content-Type", "application/json")
	forwarded.Header.Set("Origin", "https://shop.example")
	forwarded.Header.Set("X-Forwarded-Host", "shop.example")
	forwarded.Header.Set("X-Forwarded-Proto", "http")
	behindProxy := httptest.NewRecorder()
	handler.ServeHTTP(behindProxy, forwarded)
	opaque := postRealtime(handler, `{"ops":[]}`, map[string]string{"Origin": "null"})

	for name, res := range map[string]*httptest.ResponseRecorder{"http origin": plain, "forwarded http": behindProxy, "null origin": opaque} {
		if res.Code != http.StatusForbidden || res.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s = %d %v, want 403 no-store", name, res.Code, res.Header())
		}
	}
}

func TestTheHandlerSendsCORSWithEveryAnswerToAnAllowedOrigin(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler(ocel.RealtimeAllowedOrigins("https://app.example"))

	res := postRealtime(handler, `{`, map[string]string{"Origin": "https://app.example"})

	if res.Code != http.StatusBadRequest || res.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("= %d %v, want 400 with CORS", res.Code, res.Header())
	}
}

func TestTheHandlerAnswers500WhenTheBindingNamesATransportItDoesNotSpeak(t *testing.T) {
	fixture := readRealtimeFixture(t)
	fixture.Realtime.Transport = "REALTIME_TRANSPORT_UNSPECIFIED"
	deliverRealtime(t, "app", fixture)
	handler := declareHandlerResource(newHandlerRules()).Handler()

	assertServerFailure(t, postRealtime(handler, `{"ops":[{"op":"subscribe","pattern":"status"}]}`, nil))
}

func TestTheHandlerAnswers500WhenAuthorizePanics(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	resource := ocel.Realtime("app", ocel.RealtimeAuthorize(func(*http.Request) (*caller, error) { panic("secret detail") }))

	res := postRealtime(resource.Handler(), `{"ops":[]}`, nil)

	assertServerFailure(t, res)
	if strings.Contains(res.Body.String(), "secret detail") {
		t.Errorf("body = %s, want no cause", res.Body)
	}
}

func TestAuthorizeAndEveryRuleReadTheWholeRequestBody(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	var mutex sync.Mutex
	var bodies []string
	readBody := func(r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mutex.Lock()
		defer mutex.Unlock()
		bodies = append(bodies, string(raw))
	}
	resource := ocel.Realtime("app", ocel.RealtimeAuthorize(func(r *http.Request) (*caller, error) {
		readBody(r)
		return &caller{ID: "u1"}, nil
	}))
	ocel.Channel[orderEvent, orderParams](resource, "orders/:orderId",
		ocel.ChannelSubscribe(func(c *ocel.SubscribeContext[caller, orderParams]) (bool, error) {
			readBody(c.Request)
			return true, nil
		}))
	body := `{"ops":[` + strings.TrimSuffix(strings.Repeat(`{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":"o-1"}},`, 8), ",") + `]}`

	answer := readAnswer(t, postRealtime(resource.Handler(), body, nil))

	if len(answer.Grants) != 8 || len(bodies) != 9 {
		t.Fatalf("grants %d, bodies read %d, want 8 grants and authorize and 8 rules reading", len(answer.Grants), len(bodies))
	}
	for i, read := range bodies {
		if read != body {
			t.Errorf("read %d = %q, want the whole body", i, read)
		}
	}
}

func TestTheHandlerRefusesABatchWhoseShapeIsNotExactly400(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	for _, body := range []string{
		`null`, `[]`, `{"ops":null}`, `{"ops":{}}`, `{"Ops":[]}`, `{"ops":[],"extra":1}`,
		`{"ops":[],"connect":null}`, `{"ops":[],"connect":"yes"}`, `{"ops":[],"Connect":true}`,
	} {
		if res := postRealtime(handler, body, nil); res.Code != http.StatusBadRequest || res.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s = %d %v, want 400 no-store", body, res.Code, res.Header())
		}
	}
}

func TestTheHandlerDeniesEachMalformedOpOnItsOwnInSpecOrder(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	handler := declareHandlerResource(newHandlerRules()).Handler()
	cases := []struct{ op, code string }{
		{`7`, "invalid-op"},
		{`null`, "invalid-op"},
		{`{"op":"subscribe","pattern":"status","extra":1}`, "invalid-op"},
		{`{"Op":"subscribe","pattern":"status"}`, "invalid-op"},
		{`{"op":"subscribe","pattern":"status","body":{}}`, "invalid-op"},
		{`{"op":7,"pattern":"status"}`, "unknown-op"},
		{`{"pattern":"status"}`, "unknown-op"},
		{`{"op":"Subscribe","pattern":"status"}`, "unknown-op"},
		{`{"op":"subscribe","pattern":7}`, "unknown-pattern"},
		{`{"op":"subscribe"}`, "unknown-pattern"},
		{`{"op":"subscribe","pattern":"orders/:orderId","params":{"orderId":null}}`, "invalid-params"},
		{`{"op":"subscribe","pattern":"status","params":[]}`, "invalid-params"},
		{`{"op":"publish","pattern":"rooms/:roomId","params":{},"body":7}`, "missing-param"},
		{`{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"}}`, "invalid-body"},
		{`{"op":"subscribe","pattern":"status","params":null}`, ""},
		{`{"op":"subscribe","pattern":"status"}`, ""},
	}
	ops := make([]string, len(cases))
	for i, c := range cases {
		ops[i] = c.op
	}

	answer := readAnswer(t, postRealtime(handler, `{"ops":[`+strings.Join(ops, ",")+`]}`, map[string]string{"X-User": "u1"}))

	codes := map[int]string{}
	for _, denial := range answer.Denied {
		codes[denial.Index] = denial.Code
	}
	for _, grant := range answer.Grants {
		codes[grant.Index] = ""
	}
	for i, c := range cases {
		if code, answered := codes[i]; !answered || code != c.code {
			t.Errorf("op %s = %q (answered %v), want %q", c.op, code, answered, c.code)
		}
	}
}

func TestRelayedPublishesReachTheRuntimeOneAtATimeInBatchOrder(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	runtime.answerSlowly(10 * time.Millisecond)
	handler := declareHandlerResource(newHandlerRules()).Handler()
	var ops []string
	for i := range 8 {
		ops = append(ops, `{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"},"body":{"text":"`+strconv.Itoa(i)+`"}}`,
			`{"op":"subscribe","pattern":"status"}`)
	}

	answer := readAnswer(t, postRealtime(handler, `{"ops":[`+strings.Join(ops, ",")+`]}`, map[string]string{"X-User": "u1"}))

	if len(answer.Grants) != 16 {
		t.Fatalf("answer = %+v, want every op granted", answer)
	}
	events := runtime.listEvents()
	for i, event := range events {
		if want := `{"text":"` + strconv.Itoa(i) + `"}`; string(event.Envelope.Data) != want {
			t.Errorf("event %d = %s, want %s", i, event.Envelope.Data, want)
		}
	}
	if inFlight := runtime.readMaxInFlight(); len(events) != 8 || inFlight != 1 {
		t.Errorf("published %d with at most %d in flight, want 8 one at a time", len(events), inFlight)
	}
}

func TestARelayedPublishOfAnEventOutsideTheChannelSchemaIsDenied(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	resource := ocel.Realtime("app", ocel.RealtimeAuthorize(authorizeByHeader))
	ocel.Channel[chatMessage, roomParams](resource, "rooms/:roomId", ocel.ChannelSubscribePublic(),
		ocel.Schema(`{"type":"object","properties":{"text":{"type":"string","maxLength":3}}}`),
		ocel.ChannelPublish(func(c *ocel.PublishContext[caller, roomParams, chatMessage]) (bool, error) { return true, nil }))

	answer := readAnswer(t, postRealtime(resource.Handler(), `{"ops":[
		{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"},"body":{"text":"toolong"}},
		{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"},"body":{"text":"ok"}}]}`, map[string]string{"X-User": "u1"}))

	if len(answer.Denied) != 1 || answer.Denied[0].Index != 0 || answer.Denied[0].Code != "invalid-body" || len(answer.Grants) != 1 {
		t.Errorf("answer = %+v, want the long text denied invalid-body and the short one granted", answer)
	}
	if events := runtime.listEvents(); len(events) != 1 {
		t.Errorf("published = %+v, want only the short text", events)
	}
}

func TestTheHandlerServesAnAllowedOriginWithCORSAndAnswersItsPreflight(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	resource := declareHandlerResource(newHandlerRules())
	cors := resource.Handler(ocel.RealtimeAllowedOrigins("https://app.example"))

	res := postRealtime(cors, `{"ops":[]}`, map[string]string{"Origin": "https://app.example"})
	preflightReq := httptest.NewRequest(http.MethodOptions, "https://shop.example/api/realtime", nil)
	preflightReq.Header.Set("Origin", "https://app.example")
	preflight := httptest.NewRecorder()
	cors.ServeHTTP(preflight, preflightReq)
	refused := postRealtime(cors, `{"ops":[]}`, map[string]string{"Origin": "https://evil.example"})
	plain := httptest.NewRecorder()
	resource.Handler().ServeHTTP(plain, preflightReq)

	if res.Code != http.StatusOK || res.Header().Get("Access-Control-Allow-Origin") != "https://app.example" || !strings.Contains(res.Header().Get("Vary"), "Origin") {
		t.Errorf("allowed origin = %d %v, want 200 with CORS", res.Code, res.Header())
	}
	if preflight.Code != http.StatusNoContent || preflight.Header().Get("Access-Control-Allow-Methods") != "POST" ||
		preflight.Header().Get("Access-Control-Allow-Headers") != "authorization, content-type" {
		t.Errorf("preflight = %d %v, want 204 allowing POST", preflight.Code, preflight.Header())
	}
	if refused.Code != http.StatusForbidden {
		t.Errorf("other origin = %d, want 403", refused.Code)
	}
	if plain.Code != http.StatusMethodNotAllowed || plain.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight without AllowedOrigins = %d %v, want 405 without CORS", plain.Code, plain.Header())
	}
}

func TestTheHandlerAnswers500WithoutTheCauseWhenAuthorizeFails(t *testing.T) {
	deliverRealtime(t, "app", readRealtimeFixture(t))
	resource := ocel.Realtime("app", ocel.RealtimeAuthorize(func(*http.Request) (*caller, error) { return nil, errors.New("secret detail") }))

	res := postRealtime(resource.Handler(), `{"ops":[]}`, nil)

	if res.Code != http.StatusInternalServerError || strings.Contains(res.Body.String(), "secret detail") {
		t.Errorf("= %d %s, want 500 without the cause", res.Code, res.Body)
	}
}

func TestARelayedPublishRunsThePublishRuleThenPublishesFromTheServer(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	rules := newHandlerRules()
	handler := declareHandlerResource(rules).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"ops":[{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"},"body":{"text":"hi"}}]}`, map[string]string{"X-User": "u1"}))

	if answer.Transport != "ocel-gateway" || answer.Host != "" {
		t.Errorf("answer = %+v, want ocel-gateway without a host", answer)
	}
	if len(answer.Grants) != 1 || answer.Grants[0].Wire != "/app/rooms/r1" || answer.Grants[0].Token != "" {
		t.Errorf("grants = %+v, want /app/rooms/r1 granted without a token", answer.Grants)
	}
	if len(rules.publishCalls) != 1 || rules.publishCalls[0].Body.Text != "hi" || rules.publishCalls[0].Params.RoomID != "r1" {
		t.Errorf("publish rule saw %+v, want body hi in room r1", rules.publishCalls)
	}
	events := runtime.listEvents()
	if len(events) != 1 || events[0].Envelope.Channel != "/app/rooms/r1" || events[0].Envelope.Kind != "live" || string(events[0].Envelope.Data) != `{"text":"hi"}` {
		t.Errorf("published = %+v, want one live event on /app/rooms/r1", events)
	}
}

func TestARelayedPublishIsDeniedWhenItsRuleOrItsBodyRefusesIt(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	handler := declareHandlerResource(newHandlerRules()).Handler()

	invalid := readAnswer(t, postRealtime(handler, `{"ops":[{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"},"body":{"text":1}}]}`, map[string]string{"X-User": "u1"}))
	forbidden := readAnswer(t, postRealtime(handler, `{"ops":[{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"},"body":{"text":"x"}}]}`, map[string]string{"X-User": "u2"}))

	if len(invalid.Denied) != 1 || invalid.Denied[0].Code != "invalid-body" {
		t.Errorf("denied = %+v, want invalid-body", invalid.Denied)
	}
	if len(forbidden.Denied) != 1 || forbidden.Denied[0].Code != "forbidden" {
		t.Errorf("denied = %+v, want forbidden", forbidden.Denied)
	}
	if events := runtime.listEvents(); len(events) != 0 {
		t.Errorf("published = %+v, want nothing", events)
	}
}

func TestARelayedPublishTheRuntimeRefusesIsDenied(t *testing.T) {
	runtime, _ := serveFakeRealtimeRuntime(t, "app")
	runtime.refuse(connect.NewError(connect.CodeUnavailable, errors.New("the gateway refused it with status 401")))
	handler := declareHandlerResource(newHandlerRules()).Handler()

	answer := readAnswer(t, postRealtime(handler, `{"ops":[{"op":"publish","pattern":"rooms/:roomId","params":{"roomId":"r1"},"body":{"text":"hi"}}]}`, map[string]string{"X-User": "u1"}))

	if len(answer.Denied) != 1 || answer.Denied[0].Code != "publish-failed" {
		t.Errorf("denied = %+v, want publish-failed", answer.Denied)
	}
}

func assertServerFailure(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	var answer struct {
		Error string `json:"error"`
	}
	if res.Code != http.StatusInternalServerError || res.Header().Get("Cache-Control") != "no-store" ||
		json.Unmarshal(res.Body.Bytes(), &answer) != nil || answer.Error == "" {
		t.Errorf("= %d %v %s, want 500 no-store with an error", res.Code, res.Header(), res.Body)
	}
}

func TestTheHandlerAnswers500WhenAGrantTokenCannotBeMinted(t *testing.T) {
	fixture := readRealtimeFixture(t)
	fixture.Realtime.SigningKey = []byte("short")
	deliverRealtime(t, "app", fixture)
	handler := declareHandlerResource(newHandlerRules()).Handler()

	assertServerFailure(t, postRealtime(handler, `{"ops":[{"op":"subscribe","pattern":"status","params":{}}]}`, nil))
}
