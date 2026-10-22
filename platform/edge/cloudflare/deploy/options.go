package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/provider"
)

type Options struct {
	Tunnel bool `json:"tunnel,omitempty" doc:"Reach the origin through a tunnel it opens to Cloudflare, instead of at its address. A VPS always does."`
}

func (Options) Doc() string {
	return "Options for the Cloudflare edge. The token and account id are read from the environment."
}

func DecodeOptions(options provider.Options) (Options, error) {
	return provider.DecodeEdgeOptions[Options](Kind, options)
}
