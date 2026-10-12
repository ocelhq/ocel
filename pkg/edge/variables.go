package edge

const OriginClientCertificateBinding = "OCEL_ORIGIN_CLIENT_CERTIFICATE"

const (
	EdgeAccessKeyIDVar = "OCEL_EDGE_ACCESS_KEY_ID"
	EdgeSecretKeyVar   = "OCEL_EDGE_SECRET_KEY"
)

const (
	AssetStoreEndpointVar    = "OCEL_ASSET_STORE_ENDPOINT"
	AssetStoreBucketVar      = "OCEL_ASSET_STORE_BUCKET"
	AssetStorePrefixVar      = "OCEL_ASSET_STORE_PREFIX"
	AssetStoreAccessKeyIDVar = "OCEL_ASSET_STORE_ACCESS_KEY_ID"
	AssetStoreSecretKeyVar   = "OCEL_ASSET_STORE_SECRET_KEY"
)

const (
	AWSRegionVar = "OCEL_AWS_REGION"
)

const AssetBucketVar = "OCEL_ASSET_BUCKET"

const ImageOptimizerURLVar = "OCEL_IMAGE_OPTIMIZER_URL"

const StaticRulesVar = "OCEL_STATIC_RULES"

const (
	OcelRouteTableVar = "OCEL_ROUTE_TABLE"
	ReleaseTokenVar   = "OCEL_RELEASE_TOKEN"
	EdgeCacheVar      = "OCEL_EDGE_CACHE"
)

const PurgeURLVar = "OCEL_PURGE_URL"

const (
	OriginSecretVar         = "OCEL_ORIGIN_SECRET"
	OriginSecretPreviousVar = "OCEL_ORIGIN_SECRET_PREVIOUS"
	OriginSignedVar         = "OCEL_ORIGIN_SIGNED"
	OriginDispatchVar       = "OCEL_ORIGIN_DISPATCH"
	CacheTagPurgeVar        = "OCEL_CACHE_TAG_PURGE"
	OriginSecretHeader      = "x-ocel-origin-secret"

	OriginContainerHeader = "x-ocel-container"
)
