package host

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
)

const proxyServesNoCertificate = 3

func (h *Host) VerifiedPins(ctx context.Context) ([]Pin, error) {
	h.pinning.Lock()
	defer h.pinning.Unlock()
	if h.vouched {
		return slices.Clone(h.pins), h.unusable
	}
	for _, pin := range h.pins {
		if err := h.vouch(ctx, pin); err != nil {
			return nil, err
		}
	}
	h.vouched = true
	return slices.Clone(h.pins), nil
}

func (h *Host) vouch(ctx context.Context, pin Pin) error {
	refused := func(err error) error {
		h.vouched, h.unusable = true, err
		return err
	}
	if err := validPin(pin); err != nil {
		return refused(err)
	}
	block, err := h.PinnedCertificate(ctx, pin.Path)
	if err != nil {
		return err
	}
	leaf, err := certs.Parse(PinCertificate(pin.Path), block)
	if err != nil {
		return refused(err)
	}
	if err := certs.Verify(pin.Path, pin.Hostname, leaf, time.Now()); err != nil {
		return refused(err)
	}
	return nil
}

func (h *Host) PinnedCertificate(ctx context.Context, path string) ([]byte, error) {
	if !pinnedUnderProxyPins(path) {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"a pinned certificate on this box is read off %s/<name> alone, and %q is outside it: %s is the one directory bound into the proxy, so a pair anywhere else on this host is a path the proxy cannot open and one this ssh session must not open as root either",
			ProxyPins, path, ProxyPins)
	}
	read, err := h.run(ctx, "read the certificate pinned at "+PinCertificate(path),
		"cat "+quoted(PinCertificate(path)), nil)
	if err != nil {
		return nil, err
	}
	return []byte(read), nil
}

func (h *Host) CertificateTrouble(ctx context.Context, hostname string) (bool, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return false, nil
	}
	limit, said := certs.RateLimited(h.said(ctx, logCommand(ProxyContainer), elevation))
	if !said || !limit.Covers(hostname) || limit.Spent(time.Now()) {
		return true, nil
	}
	return true, limit.Refusal(hostname)
}

func (h *Host) ForgetCertificates(ctx context.Context, hostnames []string, report providerkit.Reporter) error {
	if len(hostnames) == 0 {
		return nil
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	said, err := h.ran(ctx, "forget what this box holds for "+strings.Join(hostnames, ", "),
		words(helperCommand(append([]string{"forget"}, hostnames...)...)), nil, elevation)
	if err != nil {
		return err
	}
	if report == nil {
		return nil
	}
	for line := range strings.Lines(said) {
		removed := strings.TrimSpace(line)
		if removed == "" {
			continue
		}
		report.Detail("Removed " + removed + ": the pair this box's proxy obtained for a hostname it no longer answers")
	}
	return nil
}

func (h *Host) ServedCertificate(ctx context.Context, hostname string) ([]byte, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.stream(ctx, words(helperCommand("leaf", hostname)), nil, elevation)
	if err != nil {
		return nil, err
	}
	switch result.Code {
	case 0:
		return []byte(result.Stdout), nil
	case proxyServesNoCertificate:
		return nil, nil
	default:
		return nil, h.refuse("read what the proxy serves for "+hostname, result)
	}
}
