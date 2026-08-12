package edge

import (
	"fmt"
	"strings"
)

const (
	PreviewAppSeparator = "--"

	PreviewLabelMaxLen = 63
)

func PreviewLabel(slug, pointer, app string) string {
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

func PreviewLabelProblem(slug string, hostnames []string) error {
	for _, hostname := range hostnames {
		label, _, _ := strings.Cut(hostname, ".")
		if label == "" || strings.Contains(label, "*") || len(label) <= PreviewLabelMaxLen {
			continue
		}
		return fmt.Errorf("%s is %d characters; DNS labels cap at %d — %s, %d over: shorten one of them and deploy again",
			label, len(label), PreviewLabelMaxLen, previewLabelParts(slug, label), len(label)-PreviewLabelMaxLen)
	}
	return nil
}

func previewLabelParts(slug, label string) string {
	parts := strings.Split(label, PreviewAppSeparator)
	names := []string{"preview", "app"}
	if len(parts) > 1 && parts[0] == slug {
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
