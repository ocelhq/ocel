// Package realtime declares realtime resources and their channels: what
// clients may subscribe and publish to, and what the app publishes to them.
package realtime

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"ocel.dev"
	"ocel.dev/internal/binding"
	"ocel.dev/internal/declaration"
	realtimev1 "ocel.dev/internal/proto/app/realtime/v1"
	"ocel.dev/internal/proto/app/realtime/v1/realtimev1connect"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

const defaultTokenTTL = 60 * time.Second

// An Option tunes the realtime resource [New] declares.
type Option interface {
	applyRealtime(*settings)
}

type settings struct {
	ttl       time.Duration
	authorize func(*http.Request) (any, error)
	authType  reflect.Type
}

type option func(*settings)

func (o option) applyRealtime(s *settings) { o(s) }

// Authorize identifies the caller of the realtime handler from its request,
// with whatever auth the app already uses. It answers nil for nobody; an error or a
// panic fails the whole request with 500. Its answer is the Auth of every rule, which
// a rule's context names as A, and the auth's "id" field as JSON, when a string or a
// number, is the sub of every token minted for the caller.
func Authorize[A any](authorize func(*http.Request) (*A, error)) Option {
	return option(func(s *settings) {
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

// TokenTTL is how long each token the handler mints lives, 10s to 5m in whole
// seconds; without it, 60s.
func TokenTTL(ttl time.Duration) Option {
	return option(func(s *settings) { s.ttl = ttl })
}

// A Realtime is a realtime resource an app declares: typed channels that
// browsers subscribe to through its [Realtime.Handler] and the server
// publishes on through each [Channel].
type Realtime struct {
	name     string
	source   string
	settings settings

	mutex      sync.Mutex
	channels   []*declaredChannel
	connection binding.Connection[realtimev1connect.RealtimeServiceClient]
}

// New declares a realtime resource named name, which begins every channel it
// holds: letters, digits and -, at most 50 characters. Declare its channels with
// [NewChannel] and serve them with [Realtime.Handler]. Call it from a file under
// the project's discovery folder: during discovery the call is the declaration, and at
// runtime the resource reads the binding the deploy delivered for that name.
func New(name string, opts ...Option) *Realtime {
	d := &Realtime{
		name:     name,
		source:   declaration.CallSite(0),
		settings: settings{ttl: defaultTokenTTL},
	}
	for _, opt := range opts {
		opt.applyRealtime(&d.settings)
	}
	d.declare()
	return d
}

// Name is the name the resource was declared under.
func (d *Realtime) Name() string { return d.name }

func (d *Realtime) declare() {
	channels := make([]*resourcesv1.RealtimeChannel, 0, len(d.channels))
	for _, channel := range d.channels {
		channels = append(channels, channel.buildManifest())
	}
	declaration.Declare(d.source, &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: d.name},
		Config: &resourcesv1.DeclareRequest_Realtime{Realtime: &resourcesv1.RealtimeConfig{
			Channels: channels,
			TokenTtl: durationpb.New(d.settings.ttl),
		}},
	})
}

func (d *Realtime) addChannel(channel *declaredChannel) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.channels = append(d.channels, channel)
	d.declare()
}

func (d *Realtime) findChannel(pattern string) *declaredChannel {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	for _, channel := range d.channels {
		if channel.pattern.written == pattern {
			return channel
		}
	}
	return nil
}

func (d *Realtime) readBinding(access string) (*bindingsv1.RealtimeProperties, error) {
	if declaration.Discovering() {
		return nil, &ocel.UnprovisionedError{Resource: fmt.Sprintf("realtime(%q)", d.name), Access: access}
	}
	delivered, err := binding.Read(d.name, bindingsv1.BindingType_BINDING_TYPE_REALTIME)
	if err != nil {
		return nil, err
	}
	return delivered.GetRealtime(), nil
}

func (d *Realtime) connect(access string) (realtimev1connect.RealtimeServiceClient, error) {
	return d.connection.Connect(fmt.Sprintf("realtime(%q)", d.name), access, func() (realtimev1connect.RealtimeServiceClient, error) {
		return binding.Dial(d.name, bindingsv1.BindingType_BINDING_TYPE_REALTIME, realtimev1connect.NewRealtimeServiceClient)
	})
}

func (d *Realtime) publishEvent(ctx context.Context, runtime realtimev1connect.RealtimeServiceClient, wire string, envelope []byte) error {
	if _, err := runtime.Publish(ctx, &realtimev1.PublishRequest{Realtime: d.name, Channel: wire, Event: string(envelope)}); err != nil {
		return fmt.Errorf("ocel: realtime %q: publish on %s: %w", d.name, wire, err)
	}
	return nil
}
