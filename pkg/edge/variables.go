package edge

const OriginClientCertificateBinding = "OCEL_ORIGIN_CLIENT_CERTIFICATE"

const (
	EdgeAccessKeyIDVar = "OCEL_EDGE_ACCESS_KEY_ID"
	EdgeSecretKeyVar   = "OCEL_EDGE_SECRET_KEY"
)

const (
	AWSRegionVar = "OCEL_AWS_REGION"
)

const ImageOptimizerURLVar = "OCEL_IMAGE_OPTIMIZER_URL"

const RevalidateQueueURLVar = "OCEL_REVALIDATE_QUEUE_URL"

const (
	OriginSecretVar         = "OCEL_ORIGIN_SECRET"
	OriginSecretPreviousVar = "OCEL_ORIGIN_SECRET_PREVIOUS"
	OriginSignedVar         = "OCEL_ORIGIN_SIGNED"
	OriginDispatchVar       = "OCEL_ORIGIN_DISPATCH"
	CacheTagPurgeVar        = "OCEL_CACHE_TAG_PURGE"
	OriginSecretHeader      = "x-ocel-origin-secret"

	OriginContainerHeader = "x-ocel-container"
)
