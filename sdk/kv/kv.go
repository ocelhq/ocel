// Package kv declares the key-value stores an app uses, the typed entries
// it keeps in them, and connects to them over the binding the deploy delivers.
package kv

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	"ocel.dev"
	"ocel.dev/internal/binding"
	"ocel.dev/internal/declaration"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

// ErrMiss is returned by a read of an entry key that holds no value, and by a pop of an
// empty list. Match it with errors.Is.
var ErrMiss = errors.New("ocel: the kv key holds no value")

// An InvalidValueError is returned when a value read from an entry is not a value of
// the entry's shape: a json value that does not decode into the entry's type, or a
// counter that holds no integer.
type InvalidValueError struct {
	// Key is the key whose value is invalid.
	Key string
	// Reason says what is wrong with the value.
	Reason string
}

// Error names the key and what is wrong with its value.
func (e *InvalidValueError) Error() string {
	return fmt.Sprintf("ocel: kv key %q %s", e.Key, e.Reason)
}

// An Option tunes the store [New] declares.
type Option interface {
	applyStore(*resourcesv1.KvConfig)
}

type option func(*resourcesv1.KvConfig)

func (o option) applyStore(c *resourcesv1.KvConfig) { o(c) }

// Version declares the major Valkey version the store runs, "8" or "9"; without it,
// the provider's default.
func Version(v string) Option {
	return option(func(c *resourcesv1.KvConfig) { c.Version = v })
}

// Eviction declares the policy keys are evicted by once the store is full: one of
// noeviction, allkeys-lru, allkeys-lfu, allkeys-random, volatile-lru, volatile-lfu,
// volatile-random or volatile-ttl. Without it, noeviction.
func Eviction(policy string) Option {
	return option(func(c *resourcesv1.KvConfig) { c.Eviction = policy })
}

// Memory declares the memory the store holds, which eviction acts at, written with its
// unit, such as "256mb" or "1gb". Without it, the provider's default.
func Memory(size string) Option {
	return option(func(c *resourcesv1.KvConfig) { c.Memory = size })
}

// A Store is a key-value store an app declares, one Valkey instance of its own, and
// reaches through its typed entries or its [Store.Client].
type Store struct {
	name   string
	source string

	mu     sync.Mutex
	config *resourcesv1.KvConfig

	connection binding.Connection[*redis.Client]
}

// New declares a key-value store named name and returns the handle an app reaches it
// through. Declare its entries with [NewText], [NewCounter], [NewJSON], [NewList] and
// [NewSet]. Call it from a file under the project's discovery folder: during discovery the
// call is the declaration, and at runtime the store reads the binding the deploy
// delivered for that name.
func New(name string, opts ...Option) *Store {
	config := &resourcesv1.KvConfig{}
	for _, opt := range opts {
		opt.applyStore(config)
	}
	store := &Store{name: name, source: declaration.CallSite(0), config: config}
	store.declare()
	return store
}

// Name is the name the store was declared under.
func (s *Store) Name() string { return s.name }

func (s *Store) declare() {
	if !declaration.Discovering() {
		return
	}
	declaration.Declare(s.source, &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: s.name},
		Config:   &resourcesv1.DeclareRequest_Kv{Kv: proto.CloneOf(s.config)},
	})
}

func (s *Store) addEntry(declared *resourcesv1.KvEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.Entries = append(s.config.Entries, declared)
	s.declare()
}

// ConnectionString is the store's URL, redis:// or rediss:// when it requires TLS, with
// the delivered credentials percent-encoded, for tools that take one. It fails when no
// binding was delivered for the name, and during discovery.
func (s *Store) ConnectionString() (string, error) {
	properties, err := s.properties("ConnectionString")
	if err != nil {
		return "", err
	}
	scheme := "redis"
	if properties.GetTls() {
		scheme = "rediss"
	}
	address := url.URL{
		Scheme: scheme,
		User:   url.UserPassword(properties.GetUsername(), properties.GetPassword()),
		Host:   net.JoinHostPort(properties.GetHost(), strconv.Itoa(int(properties.GetPort()))),
	}
	return address.String(), nil
}

// Client is the go-redis client connected to the store over the delivered binding,
// encrypted when the binding requires TLS, and trusting only the binding's certificate
// authority when it delivers one. The certificate is verified against the binding's
// TLS server name when it names one, as a binding pointing at a port forward does,
// and against its host otherwise. It is opened on the first call and the same
// client is returned on every one after. It fails when no binding was delivered for the
// name, when the delivered authority holds no PEM certificate, and during discovery.
func (s *Store) Client(ctx context.Context) (*redis.Client, error) {
	return s.open("Client")
}

func (s *Store) open(access string) (*redis.Client, error) {
	return s.connection.Connect(s.resource(), access, func() (*redis.Client, error) {
		return s.dial(access)
	})
}

func (s *Store) dial(access string) (*redis.Client, error) {
	properties, err := s.properties(access)
	if err != nil {
		return nil, err
	}
	options := &redis.Options{
		Addr:     net.JoinHostPort(properties.GetHost(), strconv.Itoa(int(properties.GetPort()))),
		Username: properties.GetUsername(),
		Password: properties.GetPassword(),
	}
	if properties.GetTls() {
		options.TLSConfig = &tls.Config{ServerName: cmp.Or(properties.GetTlsServerName(), properties.GetHost()), MinVersion: tls.VersionTLS12}
		if authority := properties.GetCaPem(); authority != "" {
			trusted := x509.NewCertPool()
			if !trusted.AppendCertsFromPEM([]byte(authority)) {
				return nil, fmt.Errorf("ocel: kv %q: the binding's caPem holds no PEM certificate, so nothing says which authority signs the store's certificate", s.name)
			}
			options.TLSConfig.RootCAs = trusted
		}
	}
	return redis.NewClient(options), nil
}

func (s *Store) properties(access string) (*bindingsv1.KvProperties, error) {
	if err := s.refuseDuringDiscovery(access); err != nil {
		return nil, err
	}
	delivered, err := binding.Read(s.name, bindingsv1.BindingType_BINDING_TYPE_KV)
	if err != nil {
		return nil, err
	}
	return delivered.GetKv(), nil
}

func (s *Store) refuseDuringDiscovery(access string) error {
	if declaration.Discovering() {
		return &ocel.UnprovisionedError{Resource: s.resource(), Access: access}
	}
	return nil
}

func (s *Store) resource() string {
	return fmt.Sprintf("kv(%q)", s.name)
}
