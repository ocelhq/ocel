package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"ocel.dev/internal/codec"
	realtimev1 "ocel.dev/internal/proto/app/realtime/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

const (
	maxOps          = 50
	maxRequestBytes = 1 << 20
)

type denialCode string

const (
	denialInvalidOp       denialCode = "invalid-op"
	denialUnknownOp       denialCode = "unknown-op"
	denialUnknownPattern  denialCode = "unknown-pattern"
	denialNoPublishRule   denialCode = "no-publish-rule"
	denialInvalidParams   denialCode = "invalid-params"
	denialMissingParam    denialCode = "missing-param"
	denialUnknownParam    denialCode = "unknown-param"
	denialEmptyValue      denialCode = "empty-value"
	denialValueTooLong    denialCode = "value-too-long"
	denialInvalidBody     denialCode = "invalid-body"
	denialBodyTooLarge    denialCode = "body-too-large"
	denialUnauthenticated denialCode = "unauthenticated"
	denialForbidden       denialCode = "forbidden"
	denialRuleError       denialCode = "rule-error"
	denialPublishFailed   denialCode = "publish-failed"
)

var (
	batchKeys = []string{"connect", "ops"}
	opKeys    = []string{"op", "pattern", "params", "body"}
)

// A HandlerOption tunes the handler [Realtime.Handler] serves.
type HandlerOption func(*handlerSettings)

type handlerSettings struct {
	allowedOrigins []string
}

// AllowedOrigins are origins besides the handler's own that may call it, such
// as "https://app.example". Each is answered with CORS headers; without any, only the
// handler's own origin is served and no CORS header is ever sent.
func AllowedOrigins(origins ...string) HandlerOption {
	return func(s *handlerSettings) { s.allowedOrigins = append(s.allowedOrigins, origins...) }
}

type handler struct {
	realtime *Realtime
	settings handlerSettings
}

// Handler serves the resource to browsers, mounted at any path: one batched POST of
// { connect?, ops: [{ op, pattern, params, body? }] }, at most 50 ops, answered with
// the socket url and the host its authorization names, a connect token when asked, a
// grant with a token for each op its rule allows and a denial with a code for each it
// does not. The runtime the app runs on mints every token, in one call per request, and
// names the channel each subscribe grant is for, which the browser subscribes to as
// given. A browser publish runs the channel's publish rule and is published from the
// server, one publish at a time in the order the batch lists them. It takes POST with
// application/json alone, serves its own origin and those in [AllowedOrigins], answers
// Cache-Control: no-store, and never sets a cookie.
func (d *Realtime) Handler(opts ...HandlerOption) http.Handler {
	h := &handler{realtime: d}
	for _, opt := range opts {
		opt(&h.settings)
	}
	return h
}

type realtimeBatch struct {
	connect bool
	ops     []json.RawMessage
}

type realtimeOp struct {
	operation realtimeOperation
	channel   *declaredChannel
	params    map[string]string
	body      json.RawMessage
}

type grant struct {
	Index int    `json:"i"`
	Wire  string `json:"wire"`
	Token string `json:"token,omitempty"`
}

type denial struct {
	Index int        `json:"i"`
	Code  denialCode `json:"code"`
}

type realtimeAnswer struct {
	URL     string       `json:"url"`
	Host    string       `json:"host"`
	Connect *mintedToken `json:"connect,omitempty"`
	Grants  []grant      `json:"grants"`
	Denied  []denial     `json:"denied"`
}

type realtimeOutcome struct {
	grant       *grant
	isSubscribe bool
	denial      denialCode
	err         error
}

type call struct {
	realtime *Realtime
	request  *http.Request
	body     []byte
	binding  *bindingsv1.RealtimeProperties
	auth     any
	subject  string
}

func writeResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store")
	if body == nil {
		w.WriteHeader(status)
		return
	}
	encoded, err := codec.EncodeJSON(body)
	if err != nil {
		status, encoded = http.StatusInternalServerError, []byte(`{"error":"the answer does not encode as JSON"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeResponse(w, status, map[string]string{"error": message})
}

func readFirstHeaderValue(r *http.Request, name string) string {
	first, _, _ := strings.Cut(r.Header.Get(name), ",")
	return strings.TrimSpace(first)
}

func readRequestHost(r *http.Request) string {
	if forwarded := readFirstHeaderValue(r, "X-Forwarded-Host"); forwarded != "" {
		return forwarded
	}
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

func readRequestScheme(r *http.Request) string {
	if forwarded := readFirstHeaderValue(r, "X-Forwarded-Proto"); forwarded != "" {
		return strings.ToLower(forwarded)
	}
	if r.TLS != nil || r.URL.Scheme == "https" {
		return "https"
	}
	return "http"
}

func resolveSocketURL(socket string, r *http.Request) string {
	if !strings.HasPrefix(socket, "/") {
		return socket
	}
	scheme := "ws"
	if readRequestScheme(r) == "https" {
		scheme = "wss"
	}
	return scheme + "://" + readRequestHost(r) + socket
}

func (h *handler) checkOrigin(r *http.Request) (cors http.Header, allowed bool) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil, true
	}
	if slices.Contains(h.settings.allowedOrigins, origin) {
		return http.Header{"Access-Control-Allow-Origin": {origin}, "Vary": {"Origin"}}, true
	}
	parsed, err := url.Parse(origin)
	return nil, err == nil && parsed.Host != "" && parsed.Host == readRequestHost(r) && parsed.Scheme == readRequestScheme(r)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		cors, allowed := h.checkOrigin(r)
		if !allowed {
			writeError(w, http.StatusForbidden, "this origin may not call the realtime handler")
			return
		}
		for name, values := range cors {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		h.serveBatch(w, r)
	case http.MethodOptions:
		origin := r.Header.Get("Origin")
		if origin == "" || !slices.Contains(h.settings.allowedOrigins, origin) {
			h.refuseMethod(w)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.Header().Add("Vary", "Origin")
		writeResponse(w, http.StatusNoContent, nil)
	default:
		h.refuseMethod(w)
	}
}

func (h *handler) refuseMethod(w http.ResponseWriter) {
	w.Header().Set("Allow", "POST")
	writeError(w, http.StatusMethodNotAllowed, "the realtime handler takes POST")
}

func (h *handler) serveBatch(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recover() != nil {
			writeError(w, http.StatusInternalServerError, "the realtime handler failed")
		}
	}()
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "the realtime handler takes application/json")
		return
	}
	tooLargeMessage := "a request is at most " + strconv.Itoa(maxRequestBytes) + " bytes"
	if r.ContentLength > maxRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, tooLargeMessage)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, tooLargeMessage)
		return
	}
	batch, ok := readBatch(raw)
	if err != nil || !ok {
		writeError(w, http.StatusBadRequest, "the body is { connect?: boolean, ops: [{ op, pattern, params?, body? }] }")
		return
	}
	if len(batch.ops) > maxOps {
		writeError(w, http.StatusBadRequest, "a request holds at most "+strconv.Itoa(maxOps)+" ops")
		return
	}
	delivered, err := h.realtime.readBinding("Handler")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the realtime resource has no binding this SDK can serve")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	auth, err := h.authorizeRequest(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "authorize failed")
		return
	}
	c := &call{realtime: h.realtime, request: r, body: raw, binding: delivered, auth: auth, subject: readSubject(auth)}
	answer, err := c.answerBatch(batch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the realtime handler failed")
		return
	}
	writeResponse(w, http.StatusOK, answer)
}

func (h *handler) authorizeRequest(r *http.Request) (auth any, err error) {
	authorize := h.realtime.settings.authorize
	if authorize == nil {
		return nil, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			auth, err = nil, fmt.Errorf("authorize panicked: %v", recovered)
		}
	}()
	return authorize(r)
}

func readBatch(raw []byte) (realtimeBatch, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return realtimeBatch{}, false
	}
	for key := range fields {
		if !slices.Contains(batchKeys, key) {
			return realtimeBatch{}, false
		}
	}
	var batch realtimeBatch
	if connect, given := fields["connect"]; given {
		var decoded any
		if json.Unmarshal(connect, &decoded) != nil {
			return realtimeBatch{}, false
		}
		if batch.connect, given = decoded.(bool); !given {
			return realtimeBatch{}, false
		}
	}
	if json.Unmarshal(fields["ops"], &batch.ops) != nil || batch.ops == nil {
		return realtimeBatch{}, false
	}
	return batch, true
}

func readJSONString(raw json.RawMessage) (string, bool) {
	var decoded any
	if len(raw) == 0 || json.Unmarshal(raw, &decoded) != nil {
		return "", false
	}
	text, ok := decoded.(string)
	return text, ok
}

func readParams(raw json.RawMessage) (map[string]string, bool) {
	params := map[string]string{}
	var decoded any
	if len(raw) == 0 {
		return params, true
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, false
	}
	if decoded == nil {
		return params, true
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, false
	}
	for name, value := range object {
		if params[name], ok = value.(string); !ok {
			return nil, false
		}
	}
	return params, true
}

func (d *Realtime) readOp(raw json.RawMessage) (realtimeOp, denialCode) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return realtimeOp{}, denialInvalidOp
	}
	for key := range fields {
		if !slices.Contains(opKeys, key) {
			return realtimeOp{}, denialInvalidOp
		}
	}
	name, _ := readJSONString(fields["op"])
	operation := realtimeOperation(name)
	body, hasBody := fields["body"]
	if operation == realtimeSubscribe && hasBody {
		return realtimeOp{}, denialInvalidOp
	}
	if operation != realtimeSubscribe && operation != realtimePublish {
		return realtimeOp{}, denialUnknownOp
	}
	pattern, _ := readJSONString(fields["pattern"])
	channel := d.findChannel(pattern)
	if channel == nil {
		return realtimeOp{}, denialUnknownPattern
	}
	if operation == realtimePublish && channel.settings.publish == nil {
		return realtimeOp{}, denialNoPublishRule
	}
	params, ok := readParams(fields["params"])
	if !ok {
		return realtimeOp{}, denialInvalidParams
	}
	return realtimeOp{operation: operation, channel: channel, params: params, body: body}, ""
}

func (c *call) answerBatch(batch realtimeBatch) (realtimeAnswer, error) {
	answer := realtimeAnswer{URL: resolveSocketURL(c.binding.GetUrl(), c.request), Host: c.binding.GetHost(), Grants: []grant{}, Denied: []denial{}}
	outcomes := make([]realtimeOutcome, len(batch.ops))
	var publishes []int
	ops := make([]realtimeOp, len(batch.ops))
	var evaluations sync.WaitGroup
	for i, raw := range batch.ops {
		op, denial := c.realtime.readOp(raw)
		switch {
		case denial != "":
			outcomes[i] = realtimeOutcome{denial: denial}
		case op.operation == realtimePublish:
			ops[i] = op
			publishes = append(publishes, i)
		default:
			evaluations.Go(func() { outcomes[i] = runOpCatchingPanic(func() realtimeOutcome { return c.subscribe(op) }) })
		}
	}
	evaluations.Go(func() {
		for _, i := range publishes {
			outcomes[i] = runOpCatchingPanic(func() realtimeOutcome { return c.publish(ops[i]) })
		}
	})
	evaluations.Wait()
	for _, outcome := range outcomes {
		if outcome.err != nil {
			return realtimeAnswer{}, outcome.err
		}
	}
	if err := c.mintTokens(batch.connect, &answer, outcomes); err != nil {
		return realtimeAnswer{}, err
	}

	for i, outcome := range outcomes {
		switch {
		case outcome.grant != nil:
			outcome.grant.Index = i
			answer.Grants = append(answer.Grants, *outcome.grant)
		default:
			answer.Denied = append(answer.Denied, denial{Index: i, Code: outcome.denial})
		}
	}
	return answer, nil
}

func (c *call) mintTokens(isConnect bool, answer *realtimeAnswer, outcomes []realtimeOutcome) error {
	var grants []*realtimev1.Grant
	if isConnect {
		grants = append(grants, &realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_CONNECT, Channel: "/" + c.realtime.name})
	}
	var subscribes []*grant
	for _, outcome := range outcomes {
		if outcome.isSubscribe {
			grants = append(grants, &realtimev1.Grant{Operation: realtimev1.Operation_OPERATION_SUBSCRIBE, Channel: outcome.grant.Wire})
			subscribes = append(subscribes, outcome.grant)
		}
	}
	if len(grants) == 0 {
		return nil
	}
	minted, err := c.realtime.mintTokens(c.request.Context(), c.subject, grants)
	if err != nil {
		return err
	}
	tokens := minted.GetTokens()
	if isConnect {
		answer.Connect = &mintedToken{Token: tokens[0].GetToken(), ExpiresAt: minted.GetExpiresAt().GetSeconds()}
		tokens = tokens[1:]
	}
	for i, subscribe := range subscribes {
		subscribe.Wire, subscribe.Token = tokens[i].GetChannel(), tokens[i].GetToken()
	}
	return nil
}

func runOpCatchingPanic(evaluate func() realtimeOutcome) (outcome realtimeOutcome) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = realtimeOutcome{err: fmt.Errorf("evaluating an op panicked: %v", recovered)}
		}
	}()
	return evaluate()
}

func (c *call) cloneRequest() *http.Request {
	clone := c.request.Clone(c.request.Context())
	clone.Body = io.NopCloser(bytes.NewReader(c.body))
	return clone
}

func (c *call) subscribe(op realtimeOp) realtimeOutcome {
	channel := op.channel
	wire, refused := encodeWireChannel(c.realtime.name, channel.pattern, op.params, channel.settings.wildcard)
	if refused != "" {
		return realtimeOutcome{denial: refused}
	}
	if !channel.settings.subscribePublic {
		if c.auth == nil {
			return realtimeOutcome{denial: denialUnauthenticated}
		}
		if denied := decideRule(channel.settings.subscribe, c.auth, channel.params.decodeParams(op.params), nil, c.cloneRequest()); denied != "" {
			return realtimeOutcome{denial: denied}
		}
	}
	return realtimeOutcome{grant: &grant{Wire: wire}, isSubscribe: true}
}

func (c *call) publish(op realtimeOp) realtimeOutcome {
	channel := op.channel
	wire, refused := encodeWireChannel(c.realtime.name, channel.pattern, op.params, false)
	if refused != "" {
		return realtimeOutcome{denial: refused}
	}
	if op.body == nil {
		return realtimeOutcome{denial: denialInvalidBody}
	}
	body, err := channel.decode(op.body)
	if err != nil {
		return realtimeOutcome{denial: denialInvalidBody}
	}
	envelope, refused := channel.encodeEnvelope(wire, body)
	if refused != "" {
		return realtimeOutcome{denial: refused}
	}
	if c.auth == nil {
		return realtimeOutcome{denial: denialUnauthenticated}
	}
	if denied := decideRule(channel.settings.publish, c.auth, channel.params.decodeParams(op.params), body, c.cloneRequest()); denied != "" {
		return realtimeOutcome{denial: denied}
	}
	runtime, err := c.realtime.connect("Handler")
	if err != nil {
		return realtimeOutcome{err: err}
	}
	if err := c.realtime.publishEvent(c.request.Context(), runtime, wire, envelope); err != nil {
		return realtimeOutcome{denial: denialPublishFailed}
	}
	return realtimeOutcome{grant: &grant{Wire: wire}}
}

func readSubject(auth any) string {
	if auth == nil {
		return "anonymous"
	}
	encoded, err := json.Marshal(auth)
	if err != nil {
		return "anonymous"
	}
	var identified struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(encoded, &identified) != nil || len(identified.ID) == 0 {
		return "anonymous"
	}
	var id string
	if json.Unmarshal(identified.ID, &id) == nil {
		return id
	}
	var number json.Number
	if json.Unmarshal(identified.ID, &number) == nil {
		return number.String()
	}
	return "anonymous"
}

func decideRule(rule *channelRule, auth, params, body any, r *http.Request) (denied denialCode) {
	defer func() {
		if recover() != nil {
			denied = denialRuleError
		}
	}()
	allowed, err := rule.decide(auth, params, body, r)
	switch {
	case err != nil:
		return denialRuleError
	case !allowed:
		return denialForbidden
	}
	return ""
}
