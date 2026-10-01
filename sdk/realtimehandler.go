package ocel

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
)

const (
	maxRealtimeOps          = 50
	maxRealtimeRequestBytes = 1 << 20
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
	realtimeBatchKeys = []string{"connect", "ops"}
	realtimeOpKeys    = []string{"op", "pattern", "params", "body"}
)

// A RealtimeHandlerOption tunes the handler [RealtimeDefinition.Handler] serves.
type RealtimeHandlerOption func(*handlerSettings)

type handlerSettings struct {
	allowedOrigins []string
}

// RealtimeAllowedOrigins are origins besides the handler's own that may call it, such
// as "https://app.example". Each is answered with CORS headers; without any, only the
// handler's own origin is served and no CORS header is ever sent.
func RealtimeAllowedOrigins(origins ...string) RealtimeHandlerOption {
	return func(s *handlerSettings) { s.allowedOrigins = append(s.allowedOrigins, origins...) }
}

type realtimeHandler struct {
	realtime *RealtimeDefinition
	settings handlerSettings
}

// Handler serves the resource to browsers, mounted at any path: one batched POST of
// { connect?, ops: [{ op, pattern, params, body? }] }, at most 50 ops, answered with
// the transport, its url, a connect token when asked, a grant with a token for each op
// its rule allows and a denial with a code for each it does not. A browser publish runs
// the channel's publish rule and is published from the server, one publish at a time
// in the order the batch lists them. It takes POST with application/json alone, serves
// its own origin and those in [RealtimeAllowedOrigins], answers Cache-Control:
// no-store, and never sets a cookie.
func (d *RealtimeDefinition) Handler(opts ...RealtimeHandlerOption) http.Handler {
	handler := &realtimeHandler{realtime: d}
	for _, opt := range opts {
		opt(&handler.settings)
	}
	return handler
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

type realtimeGrant struct {
	Index int    `json:"i"`
	Wire  string `json:"wire"`
	Token string `json:"token,omitempty"`
}

type realtimeDenial struct {
	Index int        `json:"i"`
	Code  denialCode `json:"code"`
}

type realtimeAnswer struct {
	Transport string           `json:"transport"`
	URL       string           `json:"url"`
	Host      string           `json:"host,omitempty"`
	Connect   *mintedToken     `json:"connect,omitempty"`
	Grants    []realtimeGrant  `json:"grants"`
	Denied    []realtimeDenial `json:"denied"`
}

type realtimeOutcome struct {
	grant  *realtimeGrant
	denial denialCode
	err    error
}

type realtimeCall struct {
	realtime *RealtimeDefinition
	request  *http.Request
	body     []byte
	binding  realtimeBinding
	auth     any
	subject  string
}

func writeRealtimeResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store")
	if body == nil {
		w.WriteHeader(status)
		return
	}
	encoded, err := encodeJSON(body)
	if err != nil {
		status, encoded = http.StatusInternalServerError, []byte(`{"error":"the answer does not encode as JSON"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func writeRealtimeError(w http.ResponseWriter, status int, message string) {
	writeRealtimeResponse(w, status, map[string]string{"error": message})
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

func (h *realtimeHandler) checkOrigin(r *http.Request) (cors http.Header, allowed bool) {
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

func (h *realtimeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		cors, allowed := h.checkOrigin(r)
		if !allowed {
			writeRealtimeError(w, http.StatusForbidden, "this origin may not call the realtime handler")
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
		writeRealtimeResponse(w, http.StatusNoContent, nil)
	default:
		h.refuseMethod(w)
	}
}

func (h *realtimeHandler) refuseMethod(w http.ResponseWriter) {
	w.Header().Set("Allow", "POST")
	writeRealtimeError(w, http.StatusMethodNotAllowed, "the realtime handler takes POST")
}

func (h *realtimeHandler) serveBatch(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recover() != nil {
			writeRealtimeError(w, http.StatusInternalServerError, "the realtime handler failed")
		}
	}()
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeRealtimeError(w, http.StatusUnsupportedMediaType, "the realtime handler takes application/json")
		return
	}
	tooLargeMessage := "a request is at most " + strconv.Itoa(maxRealtimeRequestBytes) + " bytes"
	if r.ContentLength > maxRealtimeRequestBytes {
		writeRealtimeError(w, http.StatusRequestEntityTooLarge, tooLargeMessage)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRealtimeRequestBytes))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		writeRealtimeError(w, http.StatusRequestEntityTooLarge, tooLargeMessage)
		return
	}
	batch, ok := readRealtimeBatch(raw)
	if err != nil || !ok {
		writeRealtimeError(w, http.StatusBadRequest, "the body is { connect?: boolean, ops: [{ op, pattern, params?, body? }] }")
		return
	}
	if len(batch.ops) > maxRealtimeOps {
		writeRealtimeError(w, http.StatusBadRequest, "a request holds at most "+strconv.Itoa(maxRealtimeOps)+" ops")
		return
	}
	binding, err := h.realtime.readBinding("Handler")
	if err != nil {
		writeRealtimeError(w, http.StatusInternalServerError, "the realtime resource has no binding this SDK can serve")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	auth, err := h.authorizeRequest(r)
	if err != nil {
		writeRealtimeError(w, http.StatusInternalServerError, "authorize failed")
		return
	}
	call := &realtimeCall{realtime: h.realtime, request: r, body: raw, binding: binding, auth: auth, subject: readRealtimeSubject(auth)}
	answer, err := call.answerBatch(batch)
	if err != nil {
		writeRealtimeError(w, http.StatusInternalServerError, "the realtime handler failed")
		return
	}
	writeRealtimeResponse(w, http.StatusOK, answer)
}

func (h *realtimeHandler) authorizeRequest(r *http.Request) (auth any, err error) {
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

func readRealtimeBatch(raw []byte) (realtimeBatch, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return realtimeBatch{}, false
	}
	for key := range fields {
		if !slices.Contains(realtimeBatchKeys, key) {
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

func readRealtimeParams(raw json.RawMessage) (map[string]string, bool) {
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

func (d *RealtimeDefinition) readOp(raw json.RawMessage) (realtimeOp, denialCode) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return realtimeOp{}, denialInvalidOp
	}
	for key := range fields {
		if !slices.Contains(realtimeOpKeys, key) {
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
	params, ok := readRealtimeParams(fields["params"])
	if !ok {
		return realtimeOp{}, denialInvalidParams
	}
	return realtimeOp{operation: operation, channel: channel, params: params, body: body}, ""
}

func (c *realtimeCall) answerBatch(batch realtimeBatch) (realtimeAnswer, error) {
	answer := realtimeAnswer{Transport: c.binding.transport.name, URL: c.binding.properties.GetUrl(), Grants: []realtimeGrant{}, Denied: []realtimeDenial{}}
	if c.binding.transport.answersHost {
		answer.Host = c.binding.properties.GetHost()
	}
	if batch.connect {
		token, err := c.realtime.mintToken(c.binding, c.subject, realtimeConnect, "/"+c.realtime.name)
		if err != nil {
			return realtimeAnswer{}, err
		}
		answer.Connect = &token
	}

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

	for i, outcome := range outcomes {
		switch {
		case outcome.err != nil:
			return realtimeAnswer{}, outcome.err
		case outcome.grant != nil:
			outcome.grant.Index = i
			answer.Grants = append(answer.Grants, *outcome.grant)
		default:
			answer.Denied = append(answer.Denied, realtimeDenial{Index: i, Code: outcome.denial})
		}
	}
	return answer, nil
}

func runOpCatchingPanic(evaluate func() realtimeOutcome) (outcome realtimeOutcome) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = realtimeOutcome{err: fmt.Errorf("evaluating an op panicked: %v", recovered)}
		}
	}()
	return evaluate()
}

func (c *realtimeCall) cloneRequest() *http.Request {
	clone := c.request.Clone(c.request.Context())
	clone.Body = io.NopCloser(bytes.NewReader(c.body))
	return clone
}

func (c *realtimeCall) subscribe(op realtimeOp) realtimeOutcome {
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
	token, err := c.realtime.mintToken(c.binding, c.subject, realtimeSubscribe, wire)
	if err != nil {
		return realtimeOutcome{err: err}
	}
	return realtimeOutcome{grant: &realtimeGrant{Wire: wire, Token: token.Token}}
}

func (c *realtimeCall) publish(op realtimeOp) realtimeOutcome {
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
	credential, err := c.realtime.mintPublishCredential(c.binding, wire)
	if err != nil {
		return realtimeOutcome{err: err}
	}
	if err := c.realtime.sendEvent(c.request.Context(), c.binding, wire, envelope, credential); err != nil {
		return realtimeOutcome{denial: denialPublishFailed}
	}
	return realtimeOutcome{grant: &realtimeGrant{Wire: wire}}
}

func readRealtimeSubject(auth any) string {
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
