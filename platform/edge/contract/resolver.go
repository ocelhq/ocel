package edge

type Resolver interface {
	FunctionURL(routeID string) (string, error)
	EdgeCredentials() (Credentials, bool)
}

type Credentials struct {
	AccessKeyID string
	SecretKey   string
}

const (
	EdgeAccessKeyIDVar = "OCEL_EDGE_ACCESS_KEY_ID"
	EdgeSecretKeyVar   = "OCEL_EDGE_SECRET_KEY"
)

const (
	AWSRegionVar   = "OCEL_AWS_REGION"
	StateTableVar  = "OCEL_STATE_TABLE"
	AssetBucketVar = "OCEL_ISR_BUCKET"
)

const ImageOptimizerURLVar = "OCEL_IMAGE_OPTIMIZER_URL"

const RevalidateQueueURLVar = "OCEL_REVALIDATE_QUEUE_URL"

const OriginBodyLimitVar = "OCEL_ORIGIN_BODY_LIMIT"

const (
	OriginSecretVar    = "OCEL_ORIGIN_SECRET"
	OriginSignedVar    = "OCEL_ORIGIN_SIGNED"
	OriginRouterVar    = "OCEL_ORIGIN_ROUTER"
	CacheTagPurgeVar   = "OCEL_CACHE_TAG_PURGE"
	OriginSecretHeader = "x-ocel-origin-secret"
)
