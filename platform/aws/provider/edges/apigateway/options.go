package apigateway

type Options struct{}

func (Options) Doc() string {
	return "Options for the API Gateway edge. Everything API Gateway needs comes from the provider's own options."
}
