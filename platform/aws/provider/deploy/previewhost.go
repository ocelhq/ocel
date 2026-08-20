package deploy

import (
	"fmt"
	"strings"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	previewAppSeparator = edge.PreviewAppSeparator

	previewLabelMaxLen = edge.PreviewLabelMaxLen
)

func previewHost(pointer, app, base string, singleApp bool) string {
	if pointer == "" || base == "" {
		return ""
	}
	if singleApp {
		return pointer + "." + base
	}
	return pointer + previewAppSeparator + app + "." + base
}

func firstDomain(domains []string) string {
	if len(domains) == 0 {
		return ""
	}
	return domains[0]
}

func previewBaseDomain(wildcard string) string {
	if !strings.HasPrefix(wildcard, "*.") {
		return ""
	}
	return wildcard[len("*."):]
}

func previewWildcard(base string) string {
	if base == "" {
		return ""
	}
	return "*." + base
}

type workerHostnames struct {
	hosts       map[string][]string
	previewBase string
}

type hostingWorld int

const (
	hostingProduction hostingWorld = iota
	hostingGlobalPreview
	hostingProjectPreview
)

func hostingWorldFor(cfg Config, manifest *contractv1.Manifest) hostingWorld {
	if cfg.Tier != environmentv1.Tier_TIER_PREVIEW {
		return hostingProduction
	}
	if cfg.GlobalPreviewDomain != "" && !declaresPreviewDomain(manifest) {
		return hostingGlobalPreview
	}
	return hostingProjectPreview
}

func (w hostingWorld) hostnames(cfg Config, manifest *contractv1.Manifest, apps []*contractv1.ManifestApp) (workerHostnames, error) {
	declared, err := workerDomains(cfg, manifest, apps)
	if err != nil {
		return workerHostnames{}, err
	}
	switch w {
	case hostingGlobalPreview:
		resolved, err := globalPreviewHostnames(cfg, apps)
		if err != nil {
			return workerHostnames{}, err
		}
		if err := checkPreviewLabels(cfg.Slug, apps, resolved); err != nil {
			return workerHostnames{}, err
		}
		return resolved, nil
	case hostingProjectPreview:
		resolved, err := previewHostnames(cfg, apps, declared)
		if err != nil {
			return workerHostnames{}, err
		}
		if err := checkPreviewLabels("", apps, resolved); err != nil {
			return workerHostnames{}, err
		}
		return resolved, nil
	}
	return workerHostnames{hosts: declared}, nil
}

func checkPreviewLabels(slug string, apps []*contractv1.ManifestApp, resolved workerHostnames) error {
	for _, app := range apps {
		if err := PreviewLabelProblem(slug, resolved.hosts[app.GetName()]); err != nil {
			return err
		}
	}
	return nil
}

func servesOnGlobalPreviewDomain(cfg Config, manifest *contractv1.Manifest) bool {
	return hostingWorldFor(cfg, manifest) == hostingGlobalPreview
}

func globalPreviewHostnames(cfg Config, apps []*contractv1.ManifestApp) (workerHostnames, error) {
	if err := checkPreviewPointer(cfg.Identity); err != nil {
		return workerHostnames{}, err
	}
	hosts := make(map[string][]string, len(apps))
	for _, app := range apps {
		name := app.GetName()
		qualifier := name
		if len(apps) == 1 {
			qualifier = ""
		}
		host := edge.PreviewHost(cfg.Slug, cfg.Identity, qualifier, cfg.GlobalPreviewDomain)
		if host == "" {
			continue
		}
		hosts[name] = []string{host}
	}
	return workerHostnames{hosts: hosts, previewBase: cfg.GlobalPreviewDomain}, nil
}

func checkPreviewPointer(pointer string) error {
	if strings.Contains(pointer, previewAppSeparator) {
		return fmt.Errorf("preview name %q contains %q, which separates the preview from the app in the hostname it is served on: use a single hyphen", pointer, previewAppSeparator)
	}
	return nil
}

func previewHostnames(cfg Config, apps []*contractv1.ManifestApp, declared map[string][]string) (workerHostnames, error) {
	base := ""
	for _, app := range apps {
		name := app.GetName()
		domain := firstDomain(declared[name])
		if domain == "" {
			continue
		}
		resolved := previewBaseDomain(domain)
		if resolved == "" {
			return workerHostnames{}, fmt.Errorf("app %q declares the preview domain %q, which is not a \"*.\" wildcard: every preview is served on its own subdomain of it, so declare \"*.%s\" instead", name, domain, domain)
		}
		if base != "" && resolved != base {
			return workerHostnames{}, fmt.Errorf("this project declares more than one preview domain (%q and %q): a preview domain is claimed by the whole project, which serves every app from one wildcard, so declare a single project-level domains.preview", previewWildcard(base), domain)
		}
		base = resolved
	}
	if base == "" {
		return workerHostnames{}, fmt.Errorf("this project declares no preview domain, so a preview deploy has nowhere to serve: add a project-level domains.preview wildcard (e.g. \"*.preview.acme.com\") to your ocel config and deploy again")
	}

	if err := checkPreviewPointer(cfg.Identity); err != nil {
		return workerHostnames{}, err
	}

	hosts := make(map[string][]string, len(apps))
	for _, app := range apps {
		name := app.GetName()
		host := previewHost(cfg.Identity, name, base, len(apps) == 1)
		if host == "" {
			continue
		}
		hosts[name] = []string{host}
	}
	return workerHostnames{hosts: hosts, previewBase: base}, nil
}
