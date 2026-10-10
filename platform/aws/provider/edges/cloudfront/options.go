package cloudfront

type Options struct {
	OriginShield *bool `json:"originShield,omitempty" doc:"Send the cache misses of a release with prerendered pages through CloudFront Origin Shield, so a burst of requests for an uncached page reaches the function once instead of once per edge location. On unless set to false. CloudFront bills it per request, about $0.0075 per 10,000 requests in the US. Only GET and HEAD requests to the app's function pass through it, never static files or other methods."`
}

func (Options) Doc() string {
	return "Options for the CloudFront edge. Everything CloudFront needs comes from the provider's own options."
}

func (o Options) ShieldsOrigin() bool { return o.OriginShield == nil || *o.OriginShield }
