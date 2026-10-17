package provider

import "github.com/ocelhq/ocel/pkg/environment"

type LiveApps struct {
	Slug string
	Tier environment.Tier
	Env  string
	Apps []LiveApp
}

type LiveApp struct {
	App     string
	Compute Compute
	Workers []string
}

func (l LiveApps) Find(app string) (LiveApp, bool) {
	for _, live := range l.Apps {
		if live.App == app {
			return live, true
		}
	}
	return LiveApp{}, false
}

func (l LiveApps) Has(app string) bool {
	_, deployed := l.Find(app)
	return deployed
}
