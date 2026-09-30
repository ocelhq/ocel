package alb

type Options struct{}

func (Options) Doc() string {
	return "Options for the Application Load Balancer edge. Everything the load balancer needs comes from the provider's own options."
}
