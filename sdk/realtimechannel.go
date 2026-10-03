package ocel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
)

const maxRealtimeEventBytes = 240 << 10

// A SubscribeContext is what a subscribe rule decides from.
type SubscribeContext[A, P any] struct {
	// Auth is what [RealtimeAuthorize] answered for the request; never nil in a rule.
	Auth *A
	// Params are the params the caller asked for. On a [ChannelWildcard] channel, the trailing
	// params a caller left off are "".
	Params P
	// Request is the request the realtime handler received.
	Request *http.Request
}

// A PublishContext is what a publish rule decides from: a [SubscribeContext] whose
// params are all set, and the event the caller asked to publish.
type PublishContext[A, P, E any] struct {
	SubscribeContext[A, P]
	// Body is the event, decoded from the request's JSON.
	Body E
}

// A ChannelOption tunes the channel [Channel] declares.
type ChannelOption interface {
	applyChannel(*channelSettings)
}

type channelOption func(*channelSettings)

func (o channelOption) applyChannel(s *channelSettings) { o(s) }

type channelRule struct {
	authType   reflect.Type
	paramsType reflect.Type
	bodyType   reflect.Type
	decide     func(auth any, params any, body any, r *http.Request) (bool, error)
}

type channelSettings struct {
	wildcard        bool
	schema          string
	subscribePublic bool
	subscribe       *channelRule
	publish         *channelRule
}

// ChannelSubscribe is the rule deciding whether a caller may subscribe to a channel of
// the pattern. It runs only for a caller [RealtimeAuthorize] answered for; A is the
// type it answers and P the channel's params.
func ChannelSubscribe[A, P any](rule func(*SubscribeContext[A, P]) (bool, error)) ChannelOption {
	return channelOption(func(s *channelSettings) {
		s.subscribePublic = false
		s.subscribe = &channelRule{
			authType:   reflect.TypeFor[A](),
			paramsType: reflect.TypeFor[P](),
			decide: func(auth, params, _ any, r *http.Request) (bool, error) {
				return rule(&SubscribeContext[A, P]{Auth: auth.(*A), Params: params.(P), Request: r})
			},
		}
	})
}

// ChannelSubscribePublic lets anyone subscribe to a channel of the pattern, with no
// rule and no [RealtimeAuthorize].
func ChannelSubscribePublic() ChannelOption {
	return channelOption(func(s *channelSettings) {
		s.subscribePublic = true
		s.subscribe = nil
	})
}

// ChannelPublish is the rule deciding whether a caller may publish an event from a
// browser, which the handler then publishes from the server. Without it, only the
// server publishes on the channel.
func ChannelPublish[A, P, E any](rule func(*PublishContext[A, P, E]) (bool, error)) ChannelOption {
	return channelOption(func(s *channelSettings) {
		s.publish = &channelRule{
			authType:   reflect.TypeFor[A](),
			paramsType: reflect.TypeFor[P](),
			bodyType:   reflect.TypeFor[E](),
			decide: func(auth, params, body any, r *http.Request) (bool, error) {
				return rule(&PublishContext[A, P, E]{
					SubscribeContext: SubscribeContext[A, P]{Auth: auth.(*A), Params: params.(P), Request: r},
					Body:             body.(E),
				})
			},
		}
	})
}

// ChannelWildcard lets a subscriber leave off a channel's trailing params, receiving
// every channel under the ones it gave. A publish always sets every param.
func ChannelWildcard() ChannelOption {
	return channelOption(func(s *channelSettings) { s.wildcard = true })
}

type declaredChannel struct {
	pattern  channelPattern
	source   string
	settings channelSettings
	params   channelParamsMapping
	schema   *jsonschema.Schema
	decode   func([]byte) (any, error)
}

func (c *declaredChannel) buildManifest() *resourcesv1.RealtimeChannel {
	subscribe := resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE
	if c.settings.subscribePublic {
		subscribe = resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC
	}
	publish := resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER
	if c.settings.publish != nil {
		publish = resourcesv1.RealtimePublish_REALTIME_PUBLISH_RULE
	}
	return &resourcesv1.RealtimeChannel{
		Pattern:   c.pattern.written,
		Wildcard:  c.settings.wildcard,
		Schema:    c.settings.schema,
		Subscribe: subscribe,
		Publish:   publish,
		Source:    c.source,
	}
}

// A ChannelDefinition is one channel pattern of a realtime resource, whose events are
// an E and whose params are a P.
type ChannelDefinition[E, P any] struct {
	realtime *RealtimeDefinition
	channel  *declaredChannel
}

// Channel declares the channel pattern of realtime whose events are an E, encoded as JSON,
// and whose params are a P: a struct with a string field for each :param of the
// pattern, named after it in any case or tagged `realtime:"name"`, or struct{} for a
// pattern without params. A pattern is at most 4 segments joined by /, each a literal
// or a :param. Every channel takes [ChannelSubscribe] or [ChannelSubscribePublic];
// [ChannelPublish], [ChannelWildcard] and [Schema] are optional. A declaration outside
// these rules panics.
func Channel[E, P any](realtime *RealtimeDefinition, pattern string, opts ...ChannelOption) *ChannelDefinition[E, P] {
	_, file, line, _ := runtime.Caller(1)
	source := fmt.Sprintf("%s:%d", file, line)
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("ocel: realtime %q: channel %q declared at %s ", realtime.name, pattern, source) + fmt.Sprintf(format, args...))
	}
	parsed, err := parseChannelPattern(pattern)
	if err != nil {
		fail("%v", err)
	}
	paramsType := reflect.TypeFor[P]()
	mapping, err := newChannelParamsMapping(parsed, paramsType)
	if err != nil {
		fail("%v", err)
	}
	var settings channelSettings
	for _, opt := range opts {
		opt.applyChannel(&settings)
	}
	if !settings.subscribePublic && settings.subscribe == nil {
		fail("declares no subscribe: pass ChannelSubscribe with a rule, or ChannelSubscribePublic")
	}
	eventType := reflect.TypeFor[E]()
	for _, rule := range []*channelRule{settings.subscribe, settings.publish} {
		if rule == nil {
			continue
		}
		if rule.paramsType != paramsType {
			fail("has a rule whose params are %s, and the channel's are %s", rule.paramsType, paramsType)
		}
		if realtime.settings.authType == nil {
			fail("has a rule, and the resource declares no RealtimeAuthorize to answer its auth: pass RealtimeAuthorize to Realtime")
		}
		if rule.authType != realtime.settings.authType {
			fail("has a rule whose auth is *%s, and authorize answers *%s", rule.authType, realtime.settings.authType)
		}
		if rule.bodyType != nil && rule.bodyType != eventType {
			fail("has a publish rule whose body is %s, and the channel's events are %s", rule.bodyType, eventType)
		}
	}
	schema, err := compileChannelSchema(settings.schema)
	if err != nil {
		fail("has a schema %v", err)
	}
	channel := &declaredChannel{
		pattern:  parsed,
		source:   source,
		settings: settings,
		params:   mapping,
		schema:   schema,
		decode: func(raw []byte) (any, error) {
			var event E
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&event); err != nil {
				return nil, err
			}
			return event, nil
		},
	}
	realtime.addChannel(channel)
	return &ChannelDefinition[E, P]{realtime: realtime, channel: channel}
}

// Pattern is the pattern the channel was declared under.
func (c *ChannelDefinition[E, P]) Pattern() string { return c.channel.pattern.written }

// A PublishRefusedError is returned by Publish when the params or the event cannot be
// published.
type PublishRefusedError struct {
	// Code is the reason, one of the codes the realtime handler denies an op with:
	// empty-value, value-too-long, invalid-body or body-too-large.
	Code string
	// Pattern is the channel pattern published on.
	Pattern string
}

// Error names the pattern and the reason.
func (e *PublishRefusedError) Error() string {
	return fmt.Sprintf("ocel: realtime channel %q refuses the publish: %s", e.Pattern, e.Code)
}

// Publish sends body to every subscriber of the channel params fill in the pattern.
// Every param is required. It returns a [*PublishRefusedError] for params or a body
// that cannot be published, an [*UnprovisionedError] during discovery and a
// [*MissingBindingError] when no binding was delivered.
func (c *ChannelDefinition[E, P]) Publish(ctx context.Context, params P, body E) error {
	runtime, err := c.realtime.connect("Publish")
	if err != nil {
		return err
	}
	wire, refused := encodeWireChannel(c.realtime.name, c.channel.pattern, c.channel.params.encodeParams(params), false)
	if refused != "" {
		return &PublishRefusedError{Code: string(refused), Pattern: c.channel.pattern.written}
	}
	envelope, refused := c.channel.encodeEnvelope(wire, body)
	if refused != "" {
		return &PublishRefusedError{Code: string(refused), Pattern: c.channel.pattern.written}
	}
	return c.realtime.publishEvent(ctx, runtime, wire, envelope)
}

type realtimeEnvelope struct {
	Version   int             `json:"v"`
	ID        string          `json:"id"`
	Channel   string          `json:"ch"`
	Timestamp int64           `json:"ts"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
}

func (c *declaredChannel) encodeEnvelope(wire string, body any) ([]byte, denialCode) {
	data, err := encodeJSON(body)
	if err != nil || !c.isValidEvent(data) {
		return nil, denialInvalidBody
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	envelope, err := encodeJSON(realtimeEnvelope{
		Version:   1,
		ID:        hex.EncodeToString(id),
		Channel:   wire,
		Timestamp: time.Now().UnixMilli(),
		Kind:      "live",
		Data:      data,
	})
	if err != nil {
		return nil, denialInvalidBody
	}
	if len(envelope) > maxRealtimeEventBytes {
		return nil, denialBodyTooLarge
	}
	return envelope, ""
}

func compileChannelSchema(written string) (*jsonschema.Schema, error) {
	if written == "" {
		return nil, nil
	}
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(written))
	if err != nil {
		return nil, fmt.Errorf("that is no JSON: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	const location = "urn:ocel:realtime:channel"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, fmt.Errorf("that does not compile: %w", err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("that does not compile: %w", err)
	}
	return schema, nil
}

func (c *declaredChannel) isValidEvent(data []byte) bool {
	if c.schema == nil {
		return true
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	return err == nil && c.schema.Validate(instance) == nil
}

type channelParamsMapping struct {
	fields     map[string][]int
	paramsType reflect.Type
}

func newChannelParamsMapping(pattern channelPattern, paramsType reflect.Type) (channelParamsMapping, error) {
	parameters := pattern.listParameters()
	if paramsType.Kind() != reflect.Struct {
		return channelParamsMapping{}, fmt.Errorf("has params of type %s, and params are a struct with a string field for each parameter", paramsType)
	}
	mapping := channelParamsMapping{fields: map[string][]int{}, paramsType: paramsType}
	for i := range paramsType.NumField() {
		field := paramsType.Field(i)
		if !field.IsExported() {
			continue
		}
		parameter := matchChannelParameter(field, parameters)
		if parameter == "" {
			return channelParamsMapping{}, fmt.Errorf("has params %s with field %s, which names no parameter of the pattern", paramsType, field.Name)
		}
		if field.Type.Kind() != reflect.String {
			return channelParamsMapping{}, fmt.Errorf("has params %s with field %s of type %s: each field is a string", paramsType, field.Name, field.Type)
		}
		mapping.fields[parameter] = field.Index
	}
	for _, parameter := range parameters {
		if _, ok := mapping.fields[parameter]; !ok {
			return channelParamsMapping{}, fmt.Errorf("has params %s with no field for parameter %q: name a field after it, or tag one `realtime:%q`", paramsType, parameter, parameter)
		}
	}
	return mapping, nil
}

func matchChannelParameter(field reflect.StructField, parameters []string) string {
	if tag, ok := field.Tag.Lookup("realtime"); ok {
		for _, parameter := range parameters {
			if parameter == tag {
				return parameter
			}
		}
		return ""
	}
	for _, parameter := range parameters {
		if strings.EqualFold(parameter, field.Name) {
			return parameter
		}
	}
	return ""
}

func (m channelParamsMapping) encodeParams(params any) map[string]string {
	value := reflect.ValueOf(params)
	out := make(map[string]string, len(m.fields))
	for parameter, index := range m.fields {
		out[parameter] = value.FieldByIndex(index).String()
	}
	return out
}

func (m channelParamsMapping) decodeParams(params map[string]string) any {
	value := reflect.New(m.paramsType).Elem()
	for parameter, index := range m.fields {
		value.FieldByIndex(index).SetString(params[parameter])
	}
	return value.Interface()
}
