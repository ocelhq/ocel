package edge

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

const PreviewLabelMaxLen = 63

const previewPrefixMaxLen = PreviewLabelMaxLen - 1 - PreviewTailLen

type PreviewHost struct {
	Hostname string `json:"hostname"`
	App      string `json:"app,omitempty"`
}

func (h PreviewHost) ReadLabel() string {
	label, _, _ := strings.Cut(h.Hostname, ".")
	return label
}

func (h PreviewHost) ReadPrefix() string {
	prefix, _ := h.split()
	return prefix
}

func (h PreviewHost) ReadTail() string {
	_, tail := h.split()
	return tail
}

func (h PreviewHost) split() (prefix, tail string) {
	label := h.ReadLabel()
	cut := len(label) - PreviewTailLen - 1
	if cut < 1 || label[cut] != '-' {
		return "", ""
	}
	return label[:cut], label[cut+1:]
}

func ListPreviewHostnames(hosts []PreviewHost) []string {
	names := make([]string, 0, len(hosts))
	for _, host := range hosts {
		names = append(names, host.Hostname)
	}
	return names
}

type PreviewSite struct {
	slug string
	base string
	key  PreviewKey
}

func NewSharedPreviewSite(slug, baseDomain string, key PreviewKey) PreviewSite {
	return PreviewSite{slug: slug, base: baseDomain, key: key}
}

func NewProjectPreviewSite(baseDomain string, key PreviewKey) PreviewSite {
	return PreviewSite{base: baseDomain, key: key}
}

func (s PreviewSite) IsServing() bool { return s.base != "" }

func (s PreviewSite) ListHosts(name, token string, apps []string) []PreviewHost {
	if !s.IsServing() || name == "" || token == "" {
		return nil
	}
	if len(apps) < 2 {
		host := PreviewHost{Hostname: s.signLabel(name, "", token) + "." + s.base}
		if len(apps) == 1 {
			host.App = apps[0]
		}
		return []PreviewHost{host}
	}
	hosts := make([]PreviewHost, 0, len(apps))
	for _, app := range apps {
		hosts = append(hosts, PreviewHost{Hostname: s.signLabel(name, app, deriveAppToken(token, app)) + "." + s.base, App: app})
	}
	return hosts
}

func (s PreviewSite) signLabel(name, app, token string) string {
	if s.slug != "" {
		return s.key.Sign(s.slug, token)
	}
	return s.key.Sign(fitPrefix(name, app), token)
}

func fitPrefix(name, app string) string {
	if app == "" {
		return strings.TrimRight(name[:min(len(name), previewPrefixMaxLen)], "-")
	}
	room := previewPrefixMaxLen - len(app) - 1
	if room < 1 {
		return strings.TrimRight(app[:min(len(app), previewPrefixMaxLen)], "-")
	}
	return strings.TrimRight(name[:min(len(name), room)], "-") + "-" + app
}

func deriveAppToken(token, app string) string {
	sum := sha256.Sum256([]byte(token + "/" + app))
	return previewEncoding.EncodeToString(sum[:])[:PreviewTokenLen]
}

func (s PreviewSite) RefuseOverlongLabels(hosts []PreviewHost) error {
	for _, host := range hosts {
		label := host.ReadLabel()
		if len(label) <= PreviewLabelMaxLen {
			continue
		}
		return fmt.Errorf("%s is %d characters; DNS labels cap at %d — project %q (%d) + a token (%d), %d over: shorten the project slug and deploy again",
			label, len(label), PreviewLabelMaxLen, s.slug, len(s.slug), PreviewTailLen+1, len(label)-PreviewLabelMaxLen)
	}
	return nil
}
