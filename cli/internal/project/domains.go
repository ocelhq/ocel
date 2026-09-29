package project

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
)

func normalizeProjectDomains(raw *configdoc.ProjectDomainConfig) (map[string][]string, error) {
	if raw == nil {
		return map[string][]string{}, nil
	}
	return normalizeDomains(raw.Production, raw.Preview)
}

func normalizeDomains(production configdoc.StringList, rawPreview string) (map[string][]string, error) {
	domains := map[string][]string{}

	var preview string
	if rawPreview != "" {
		preview = strings.ToLower(rawPreview)
		if err := ValidatePreviewDomain(preview); err != nil {
			return nil, err
		}
		domains["preview"] = []string{preview}
	}

	hosts, err := normalizeProductionDomains(production, preview)
	if err != nil {
		return nil, err
	}
	if len(hosts) > 0 {
		domains["production"] = hosts
	}

	return domains, nil
}

func normalizeProductionDomains(raw configdoc.StringList, preview string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, d := range raw {
		host := strings.ToLower(strings.TrimSpace(d))
		if host == "" {
			continue
		}
		if preview != "" && host == preview {
			return nil, fmt.Errorf("production domain %q is identical to the preview wildcard %q; production and preview cannot be served on the same hostname pattern — give them different hostnames", host, preview)
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out, nil
}

func ValidatePreviewDomain(domain string) error {
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return fmt.Errorf("preview domain %q must be a wildcard hostname like \"*.preview.example.com\"", domain)
	}
	if strings.Count(domain, "*") != 1 {
		return fmt.Errorf("preview domain %q must contain exactly one wildcard, in its leftmost label (e.g. \"*.preview.example.com\")", domain)
	}
	if labels[0] != "*" {
		return fmt.Errorf("preview domain %q must have its wildcard as the whole leftmost label (e.g. \"*.preview.example.com\")", domain)
	}
	return nil
}

func PreviewBaseDomain(previewDomain string) string {
	if !strings.HasPrefix(previewDomain, "*.") {
		return ""
	}
	return previewDomain[len("*."):]
}
