package provider

import "github.com/ocelhq/ocel/pkg/environment"

type Lease struct {
	Tier    environment.Tier
	Project string
	Env     string
}

func (l Lease) Covers(ref StackRef) bool {
	return l.Project != "" && l.Tier == ref.Tier && l.Project == ref.Project && (l.Env == "" || l.Env == ref.Name.Env)
}
