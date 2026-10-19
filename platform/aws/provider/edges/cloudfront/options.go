package cloudfront

type Options struct {
	OriginShield *bool `json:"originShield,omitempty" doc:"Route cache misses for prerendered pages through CloudFront Origin Shield. Left off, true."`
}

func (Options) Doc() string {
	return "Options for the CloudFront edge."
}

func (o Options) ShieldsOrigin() bool { return o.OriginShield == nil || *o.OriginShield }
