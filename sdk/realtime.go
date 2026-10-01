package ocel

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

const (
	minRealtimeTokenTTL     = 10 * time.Second
	maxRealtimeTokenTTL     = 5 * time.Minute
	defaultRealtimeTokenTTL = 60 * time.Second
)

// A RealtimeOption tunes the realtime resource [Realtime] declares.
type RealtimeOption interface {
	applyRealtime(*realtimeSettings)
}

type realtimeSettings struct {
	ttl       time.Duration
	authorize func(*http.Request) (any, error)
	authType  reflect.Type
}

type realtimeOption func(*realtimeSettings)

func (o realtimeOption) applyRealtime(s *realtimeSettings) { o(s) }

// RealtimeAuthorize identifies the caller of the realtime handler from its request,
// with whatever auth the app already uses. It answers nil for nobody; an error or a
// panic fails the whole request with 500. Its answer is the Auth of every rule, which
// a rule's context names as A, and the auth's "id" field as JSON, when a string or a
// number, is the sub of every token minted for the caller.
func RealtimeAuthorize[A any](authorize func(*http.Request) (*A, error)) RealtimeOption {
	return realtimeOption(func(s *realtimeSettings) {
		s.authType = reflect.TypeFor[A]()
		s.authorize = func(r *http.Request) (any, error) {
			auth, err := authorize(r)
			if err != nil || auth == nil {
				return nil, err
			}
			return auth, nil
		}
	})
}

// RealtimeTokenTTL is how long each token the handler mints lives, 10s to 5m in whole
// seconds; without it, 60s.
func RealtimeTokenTTL(ttl time.Duration) RealtimeOption {
	return realtimeOption(func(s *realtimeSettings) { s.ttl = ttl })
}

// A RealtimeDefinition is a realtime resource an app declares: typed channels that
// browsers subscribe to through its [RealtimeDefinition.Handler] and the server
// publishes on through each [ChannelDefinition].
type RealtimeDefinition struct {
	name     string
	source   string
	settings realtimeSettings

	mutex    sync.Mutex
	channels []*declaredChannel
}

// Realtime declares a realtime resource named name, which begins every channel it
// holds: letters, digits and -, at most 50 characters. Declare its channels with
// [Channel] and serve them with [RealtimeDefinition.Handler]. Call it from a file under
// the project's discovery folder: during discovery the call is the declaration, and at
// runtime the resource reads the binding the deploy delivered for that name.
func Realtime(name string, opts ...RealtimeOption) *RealtimeDefinition {
	_, file, line, _ := runtime.Caller(1)
	d := &RealtimeDefinition{
		name:     name,
		source:   fmt.Sprintf("%s:%d", file, line),
		settings: realtimeSettings{ttl: defaultRealtimeTokenTTL},
	}
	for _, opt := range opts {
		opt.applyRealtime(&d.settings)
	}
	if !channelSegment.MatchString(name) {
		d.refuse("the name begins every channel, so it is a channel namespace: %s", channelSegmentRule)
	}
	if ttl := d.settings.ttl; ttl < minRealtimeTokenTTL || ttl > maxRealtimeTokenTTL || ttl%time.Second != 0 {
		d.refuse("token ttl %s is outside %s to %s in whole seconds: a token lives long enough to open a socket and no longer", ttl, minRealtimeTokenTTL, maxRealtimeTokenTTL)
	}
	d.declare()
	return d
}

// Name is the name the resource was declared under.
func (d *RealtimeDefinition) Name() string { return d.name }

func (d *RealtimeDefinition) refuse(format string, args ...any) {
	panic(fmt.Sprintf("ocel: realtime %q: ", d.name) + fmt.Sprintf(format, args...))
}

func (d *RealtimeDefinition) declare() {
	channels := make([]*resourcesv1.RealtimeChannel, 0, len(d.channels))
	for _, channel := range d.channels {
		channels = append(channels, channel.buildManifest())
	}
	declareResource(d.source, &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: d.name},
		Config: &resourcesv1.DeclareRequest_Realtime{Realtime: &resourcesv1.RealtimeConfig{
			Channels: channels,
			TokenTtl: durationpb.New(d.settings.ttl),
		}},
	})
}

func (d *RealtimeDefinition) addChannel(channel *declaredChannel) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	for _, prior := range d.channels {
		if prior.pattern.written == channel.pattern.written {
			d.refuse("channel %q is declared already at %s, and a resource declares each pattern once", channel.pattern.written, prior.source)
		}
	}
	d.channels = append(d.channels, channel)
	d.declare()
}

func (d *RealtimeDefinition) findChannel(pattern string) *declaredChannel {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	for _, channel := range d.channels {
		if channel.pattern.written == pattern {
			return channel
		}
	}
	return nil
}

type realtimeBinding struct {
	properties *bindingsv1.RealtimeProperties
	transport  realtimeTransport
}

func (d *RealtimeDefinition) readBinding(access string) (realtimeBinding, error) {
	if discovering() {
		return realtimeBinding{}, &UnprovisionedError{Resource: fmt.Sprintf("realtime(%q)", d.name), Access: access}
	}
	delivered, err := binding(d.name, bindingsv1.BindingType_BINDING_TYPE_REALTIME)
	if err != nil {
		return realtimeBinding{}, err
	}
	properties := delivered.GetRealtime()
	transport, err := findRealtimeTransport(properties.GetTransport())
	if err != nil {
		return realtimeBinding{}, fmt.Errorf("ocel: realtime %q: %w", d.name, err)
	}
	return realtimeBinding{properties: properties, transport: transport}, nil
}
