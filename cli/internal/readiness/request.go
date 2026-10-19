package readiness

import (
	"slices"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func newPreflightRequest(cfg *project.Project, req Request) *contractv1.PreflightRequest {
	var wanted []string
	if req.Part != "" {
		wanted = []string{req.Part}
	}
	return &contractv1.PreflightRequest{
		RequiredTier:     req.Tier,
		Slug:             req.Slug,
		Domains:          req.Domains,
		DomainApps:       domainApps(cfg, req),
		Containers:       containers(cfg),
		Edge:             cfg.EdgeSelection(),
		CheckHosts:       req.CheckHosts,
		HostCheckDomains: req.HostCheckDomains,
		ProjectRegistry:  req.Registry,

		CheckPreviewRoute: req.CheckPreviewRoute,
		CheckForwarding:   req.CheckForwarding,
		RecordWants:       req.RecordWants && !req.DryRun,
		WantedParts:       wanted,
	}
}

func domainApps(cfg *project.Project, req Request) map[string]string {
	apps := map[string]string{}
	for _, host := range cfg.Hostnames(req.Tier) {
		if host.App != "" && slices.Contains(req.Domains, host.Name) {
			apps[host.Name] = host.App
		}
	}
	if len(apps) == 0 {
		return nil
	}
	return apps
}

func containers(cfg *project.Project) []*contractv1.ContainerApp {
	var named []*contractv1.ContainerApp
	for _, app := range build.ImageApps(cfg.Apps) {
		named = append(named, &contractv1.ContainerApp{App: app.Name, Arch: app.Arch})
	}
	return named
}
