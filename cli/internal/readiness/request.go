package readiness

import (
	"slices"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func newPreflightRequest(cfg *project.Project, req Request) *contractv1.PreflightRequest {
	return &contractv1.PreflightRequest{
		RequiredTier:     req.Tier,
		Slug:             req.Slug,
		Domains:          req.Domains,
		Frameworks:       frameworks(cfg),
		Containers:       containers(cfg),
		Edge:             cfg.EdgeSelection(),
		CheckHosts:       req.CheckHosts,
		HostCheckDomains: req.HostCheckDomains,
	}
}

func frameworks(cfg *project.Project) []string {
	var named []string
	for _, app := range cfg.Apps {
		if framework := app.Framework(); framework != "" {
			named = append(named, framework)
		}
	}
	slices.Sort(named)
	return slices.Compact(named)
}

func containers(cfg *project.Project) []*contractv1.ContainerApp {
	var named []*contractv1.ContainerApp
	for _, app := range build.ImageApps(cfg.Apps) {
		named = append(named, &contractv1.ContainerApp{App: app.Name, Arch: app.Arch})
	}
	return named
}
