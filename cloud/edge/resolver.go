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

// Worker binding names for the account-global stores the worker's cache
// entrypoint addresses under those credentials: the region they live in, the
// state table holding ISR tag records, and the asset bucket holding fetch-cache
// entries. The table and bucket names match what the Lambda tier reads them
// under, so one vocabulary spans both tiers. The region is bound rather than
// parsed back out of a Function URL host, which an all-edge app has none of.
//
// Nothing here is per-deployment — bootstrap provisions one table and one bucket
// for every project, app and deployment in the account — so they ride as worker
// vars beside the credentials rather than in each Deployment record, which would
// copy a constant into every record and make a bootstrap-level change need a
// redeploy of every app to propagate. What scopes them to one app is the ISR
// prefix the record already carries; the tag namespace derives from that prefix,
// so it needs no binding of its own.
const (
	AWSRegionVar   = "OCEL_AWS_REGION"
	StateTableVar  = "OCEL_STATE_TABLE"
	AssetBucketVar = "OCEL_ISR_BUCKET"
)

// ImageOptimizerURLVar names the substrate's image optimizer Function URL, which
// the worker POSTs a validated /_next/image request to and signs with the same
// edge credentials as every other Function-URL forward.
//
// A worker var and not a field of the routing manifest: the manifest describes
// one build, and one optimizer serves every project, app and deployment in the
// substrate — it is a property of the substrate, like the stores above. Bootstrap
// raises it before any app deploys, so the URL exists by the time a worker is
// uploaded. Left unbound where a substrate has no optimizer, which is what keeps
// every valid image request the 502 it was before one existed.
const ImageOptimizerURLVar = "OCEL_IMAGE_OPTIMIZER_URL"

// RevalidateQueueURLVar names the substrate's ISR revalidation queue, which the
// worker sends an admitted background refresh to instead of rendering it
// through the origin. The queue deduplicates the same render across every colo
// that asked for it, and its consumer drains them at a bounded concurrency.
//
// A substrate-level var for the same reason as the stores above: one queue
// serves every project, app and deployment in the account, and the ISR prefix
// the Deployment record already carries is what scopes a message to one build.
// The region is derived in the worker from the URL's own host rather than bound
// separately.
//
// Bound only where the substrate has a consumer to drain the queue — bootstrap
// publishes the URL only then, which is the whole of the gate. Unbound, the
// worker constructs no enqueue path and every admitted refresh renders through
// the origin exactly as it did before this queue existed. Bound against a queue
// nothing drains, the send succeeds, the refresh reports landed, the colo
// sentinel re-arms, and the route stops revalidating until it hard-expires.
const RevalidateQueueURLVar = "OCEL_REVALIDATE_QUEUE_URL"
