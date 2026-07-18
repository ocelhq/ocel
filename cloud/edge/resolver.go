package edge

// Resolver hands a framework's worker exactly the deploy outputs it asks for,
// rather than every output in the deploy. An unresolvable lookup is an error so
// a missing value fails the deploy instead of silently producing a worker that
// cannot route.
type Resolver interface {
	// FunctionURL returns the public URL serving a framework-native route id.
	FunctionURL(routeID string) (string, error)
	// CacheStore returns where this app's incremental cache lives, and whether it
	// is configured at all. Not-configured is not an error: a provider whose
	// bootstrap predates edge credentials, or an app with no prerendered content,
	// simply has no cache for the edge to read, and the worker forwards to the
	// provider's compute exactly as it otherwise would.
	CacheStore() (CacheStore, bool, error)
}

// CacheStore describes a framework's incremental cache in terms any provider
// able to supply an object store and a tag clock can satisfy: a bucket and key
// prefix holding the entries, and a table plus namespace holding the tag
// records that invalidate them.
type CacheStore struct {
	Bucket        string
	Prefix        string
	Region        string
	TagTable      string
	TagTableIndex string
	TagNamespace  string
	// Credentials sign the edge's direct reads. Zero when the edge runs inside
	// the provider's trust boundary and needs none.
	Credentials Credentials
}

// Credentials are the static keys an edge outside the provider's trust boundary
// signs its direct cache reads with.
type Credentials struct {
	AccessKeyID string
	SecretKey   string
}
