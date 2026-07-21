package edge

// Resolver hands a framework's worker exactly the deploy outputs it asks for,
// rather than every output in the deploy. An unresolvable lookup is an error so
// a missing value fails the deploy instead of silently producing a worker that
// cannot route.
type Resolver interface {
	// FunctionURL returns the URL serving a framework-native route id. The
	// Lambda behind it is provisioned with AWS_IAM auth, so the worker signs its
	// forwards with the edge credentials below.
	FunctionURL(routeID string) (string, error)
	// EdgeCredentials returns the edge reader's IAM credentials, and whether they
	// are configured. The worker signs every Function-URL forward with them
	// (SigV4). Not-configured is not an error: an edge inside the provider's
	// trust boundary needs none, and its Function URLs are not IAM-gated.
	EdgeCredentials() (Credentials, bool)
}

// Credentials are the static IAM keys an edge outside the provider's trust
// boundary signs its Function-URL forwards with. Zero when the edge runs inside
// the provider's trust boundary and needs none.
type Credentials struct {
	AccessKeyID string
	SecretKey   string
}

// Worker binding names for the edge reader's IAM credentials, read by the
// worker to SigV4-sign its Function-URL forwards (workers/nextjs/src/index.ts
// Env). The access key rides as a plain var; the secret key as a secret binding
// so it never appears in plaintext upload metadata. This is the contract shared
// by the framework-assembled worker (AssembleCloudflare) and the frozen generic
// worker (the production root stack), so it lives here rather than in either.
const (
	EdgeAccessKeyIDVar = "OCEL_EDGE_ACCESS_KEY_ID"
	EdgeSecretKeyVar   = "OCEL_EDGE_SECRET_KEY"
)
