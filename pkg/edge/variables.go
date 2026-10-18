package edge

const ImageOptimizerURLVar = "OCEL_IMAGE_OPTIMIZER_URL"

const StaticRulesVar = "OCEL_STATIC_RULES"

const (
	OcelRouteTableVar = "OCEL_ROUTE_TABLE"
	ReleaseTokenVar   = "OCEL_RELEASE_TOKEN"
	EdgeCacheVar      = "OCEL_EDGE_CACHE"

	EdgeCacheControlHeaderVar = "OCEL_EDGE_CACHE_CONTROL_HEADER"
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
