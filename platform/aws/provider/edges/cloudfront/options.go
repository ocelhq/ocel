package cloudfront

type Options struct{}

func (Options) Doc() string {
	return "Options for the CloudFront edge. Everything CloudFront needs comes from the provider's own options."
}
