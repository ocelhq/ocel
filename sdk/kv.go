package ocel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"sync"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

// ErrKVMiss is returned by a read of an entry key that holds no value, and by a pop of an
// empty list. Match it with errors.Is.
var ErrKVMiss = errors.New("ocel: the kv key holds no value")

// An InvalidKVValueError is returned when a value read from an entry is not a value of
// the entry's shape: a json value that does not decode into the entry's type, or a
// counter that holds no integer.
type InvalidKVValueError struct {
	// Key is the key whose value is invalid.
	Key string
	// Reason says what is wrong with the value.
	Reason string
}

// Error names the key and what is wrong with its value.
func (e *InvalidKVValueError) Error() string {
	return fmt.Sprintf("ocel: kv key %q %s", e.Key, e.Reason)
}

// A KVOption tunes the store [KV] declares.
type KVOption func(*resourcesv1.KvConfig)

// KVVersion declares the major Valkey version the store runs, "8" or "9"; without it,
// the provider's default.
func KVVersion(v string) KVOption {
	return func(c *resourcesv1.KvConfig) { c.Version = v }
}

// KVEviction declares the policy keys are evicted by once the store is full: one of
// noeviction, allkeys-lru, allkeys-lfu, allkeys-random, volatile-lru, volatile-lfu,
// volatile-random or volatile-ttl. Without it, noeviction.
func KVEviction(policy string) KVOption {
	return func(c *resourcesv1.KvConfig) { c.Eviction = policy }
}

// KVMemory declares the memory the store holds, which eviction acts at, written with its
// unit, such as "256mb" or "1gb". Without it, the provider's default.
func KVMemory(size string) KVOption {
	return func(c *resourcesv1.KvConfig) { c.Memory = size }
}

var kvEntryNameGrammar = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,62}$`)

var reservedKVEntryNames = map[string]bool{"client": true, "connectionString": true, "connection_string": true, "then": true, "constructor": true}

type kvDeclaredEntry struct {
	name    string
	pattern kvPattern
	source  string
}

// A KVStore is a key-value store an app declares, one Valkey instance of its own, and
// reaches through its typed entries or its [KVStore.Client].
type KVStore struct {
	name   string
	source string

	mu       sync.Mutex
	config   *resourcesv1.KvConfig
	declared []kvDeclaredEntry

	client *redis.Client
}

// KV declares a key-value store named name and returns the handle an app reaches it
// through. Declare its entries with [KVText], [KVCounter], [KVJSON], [KVList] and
// [KVSet]. Call it from a file under the project's discovery folder: during discovery the
// call is the declaration, and at runtime the store reads the binding the deploy
// delivered for that name.
func KV(name string, opts ...KVOption) *KVStore {
	config := &resourcesv1.KvConfig{}
	for _, opt := range opts {
		opt(config)
	}
	_, file, line, _ := runtime.Caller(1)
	store := &KVStore{name: name, source: fmt.Sprintf("%s:%d", file, line), config: config}
	store.declare()
	return store
}

// Name is the name the store was declared under.
func (s *KVStore) Name() string { return s.name }

func (s *KVStore) declare() {
	if !discovering() {
		return
	}
	err := declare(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: s.name},
		Config:   &resourcesv1.DeclareRequest_Kv{Kv: proto.CloneOf(s.config)},
		Source:   s.source,
	})
	if err != nil {
		panic(fmt.Sprintf("ocel: declare kv %q: %v", s.name, err))
	}
}

func (s *KVStore) addEntry(entry kvDeclaredEntry, shape resourcesv1.KvShape) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !kvEntryNameGrammar.MatchString(entry.name) {
		panic(fmt.Sprintf("ocel: kv %q: entry name %q is no name every SDK can hold: it starts with a letter and goes on in letters, digits and _, at most 63 characters", s.name, entry.name))
	}
	if reservedKVEntryNames[entry.name] {
		panic(fmt.Sprintf("ocel: kv %q: entry name %q is reserved, since a store already has a member of that name in some SDK: name the entry otherwise", s.name, entry.name))
	}
	for _, prior := range s.declared {
		if prior.name == entry.name {
			panic(fmt.Sprintf("ocel: kv %q: entry %q is declared already at %s, and a store names each entry once", s.name, entry.name, prior.source))
		}
		if prior.pattern.overlaps(entry.pattern) {
			panic(fmt.Sprintf(
				"ocel: kv %q: entry %q declared at %s has pattern %q, which overlaps pattern %q of entry %q declared at %s: some key would match both, so neither entry could tell its keys from the other's",
				s.name, entry.name, entry.source, entry.pattern.written, prior.pattern.written, prior.name, prior.source,
			))
		}
	}
	s.declared = append(s.declared, entry)
	s.config.Entries = append(s.config.Entries, &resourcesv1.KvEntry{
		Name:    entry.name,
		Pattern: entry.pattern.written,
		Shape:   shape,
		Source:  entry.source,
	})
	s.declare()
}

// ConnectionString is the store's URL, redis:// or rediss:// when it requires TLS, with
// the delivered credentials percent-encoded, for tools that take one. It fails when no
// binding was delivered for the name, and during discovery.
func (s *KVStore) ConnectionString() (string, error) {
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
// authority when it delivers one. It is opened on the first call and the same
// client is returned on every one after. It fails when no binding was delivered for the
// name, and during discovery.
func (s *KVStore) Client(ctx context.Context) (*redis.Client, error) {
	return s.open("Client")
}

func (s *KVStore) open(access string) (*redis.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
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
		options.TLSConfig = &tls.Config{ServerName: properties.GetHost(), MinVersion: tls.VersionTLS12}
		if authority := properties.GetCaPem(); authority != "" {
			trusted := x509.NewCertPool()
			if !trusted.AppendCertsFromPEM([]byte(authority)) {
				return nil, fmt.Errorf("ocel: kv %q: the binding's caPem holds no PEM certificate, so nothing says which authority signs the store's certificate", s.name)
			}
			options.TLSConfig.RootCAs = trusted
		}
	}
	s.client = redis.NewClient(options)
	return s.client, nil
}

func (s *KVStore) properties(access string) (*bindingsv1.KvProperties, error) {
	if err := s.refuseDuringDiscovery(access); err != nil {
		return nil, err
	}
	delivered, err := binding(s.name, bindingsv1.BindingType_BINDING_TYPE_KV)
	if err != nil {
		return nil, err
	}
	return delivered.GetKv(), nil
}

func (s *KVStore) refuseDuringDiscovery(access string) error {
	if discovering() {
		return &UnprovisionedError{Resource: s.resource(), Access: access}
	}
	return nil
}

func (s *KVStore) resource() string {
	return fmt.Sprintf("kv(%q)", s.name)
}
