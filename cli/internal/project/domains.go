package project

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
)

type Domains struct {
	Production []string
	Preview    string
}

func normalizeProjectDomains(raw *configdoc.ProjectDomainConfig) (Domains, error) {
	if raw == nil {
		return Domains{}, nil
	}
	preview := strings.ToLower(raw.Preview)
	if preview != "" {
		if err := ValidatePreviewDomain(preview); err != nil {
			return Domains{}, err
		}
	}
	production, err := normalizeProductionDomains(raw.Production, preview)
	if err != nil {
		return Domains{}, err
	}
	return Domains{Production: production, Preview: preview}, nil
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
