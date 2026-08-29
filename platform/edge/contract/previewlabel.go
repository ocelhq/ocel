package edge

import (
	"fmt"
	"strings"
)

const (
	PreviewAppSeparator = "--"

	PreviewLabelMaxLen = 63
)

type PreviewSite struct {
	slug string
	base string
}

func SharedPreview(slug, baseDomain string) PreviewSite {
	return PreviewSite{slug: slug, base: baseDomain}
}

func ProjectPreview(baseDomain string) PreviewSite {
	return PreviewSite{base: baseDomain}
}

func (s PreviewSite) Serves() bool { return s.base != "" }

func (s PreviewSite) Host(pointer, app string) string {
	return previewHost(s.slug, pointer, app, s.base)
}

func (s PreviewSite) Hosts(pointer string, apps []string) []string {
	if len(apps) < 2 {
		if host := s.Host(pointer, ""); host != "" {
			return []string{host}
		}
		return nil
	}
	hosts := make([]string, 0, len(apps))
	for _, app := range apps {
		if host := s.Host(pointer, app); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func (s PreviewSite) LabelProblem(hostnames []string) error {
	for _, hostname := range hostnames {
		label, _, _ := strings.Cut(hostname, ".")
		if label == "" || strings.Contains(label, "*") || len(label) <= PreviewLabelMaxLen {
			continue
		}
		return fmt.Errorf("%s is %d characters; DNS labels cap at %d — %s, %d over: shorten one of them and deploy again",
			label, len(label), PreviewLabelMaxLen, s.labelParts(label), len(label)-PreviewLabelMaxLen)
	}
	return nil
}

func (s PreviewSite) labelParts(label string) string {
	parts := strings.Split(label, PreviewAppSeparator)
	names := []string{"preview", "app"}
	if len(parts) > 1 && parts[0] == s.slug {
		names = []string{"project", "preview", "app"}
	}
	described := make([]string, 0, len(parts))
	for i, part := range parts {
		name := "name"
		if i < len(names) {
			name = names[i]
		}
		described = append(described, fmt.Sprintf("%s %q (%d)", name, part, len(part)))
	}
	return strings.Join(described, " + ")
}

func previewLabel(slug, pointer, app string) string {
	if pointer == "" {
		return ""
	}
	parts := make([]string, 0, 3)
	if slug != "" {
		parts = append(parts, slug)
	}
	parts = append(parts, pointer)
	if app != "" {
		parts = append(parts, app)
	}
	return strings.Join(parts, PreviewAppSeparator)
}

func previewHost(slug, pointer, app, baseDomain string) string {
	label := previewLabel(slug, pointer, app)
	if label == "" || baseDomain == "" {
		return ""
	}
	return label + "." + baseDomain
}
