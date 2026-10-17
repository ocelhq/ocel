package provider

import "github.com/ocelhq/ocel/pkg/environment"

type LiveApps struct {
	Slug string
	Tier environment.Tier
	Env  string
	Apps []string
}
