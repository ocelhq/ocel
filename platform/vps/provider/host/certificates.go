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
	if err := h.paired(ctx, pin); err != nil {
		return refused(err)
	}
	return nil
}

const (
	pairMatched    = "matched"
	pairMismatched = "mismatched"
	pairUnchecked  = "unchecked"
)

func pairCommand(path string) string {
	certificate, key := quoted(PinCertificate(path)), quoted(PinKey(path))
	return "if ! command -v openssl >/dev/null 2>&1; then echo " + pairUnchecked + "; exit 0; fi\n" +
		"held=$(openssl x509 -in " + certificate + " -noout -pubkey 2>/dev/null | openssl pkey -pubin -outform DER 2>/dev/null | sha256sum)\n" +
		"keyed=$(openssl pkey -in " + key + " -pubout -outform DER 2>/dev/null | sha256sum)\n" +
		"if [ -n \"$held\" ] && [ \"$held\" = \"$keyed\" ]; then echo " + pairMatched + "; else echo " + pairMismatched + "; fi"
}

func (h *Host) paired(ctx context.Context, pin Pin) error {
	said, err := h.run(ctx, "check the key pinned beside "+PinCertificate(pin.Path), pairCommand(pin.Path), nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(said) == pairMismatched {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the key at %s does not match the certificate at %s",
			PinKey(pin.Path), PinCertificate(pin.Path))
	}
	return nil
}

func (h *Host) PinnedCertificate(ctx context.Context, path string) ([]byte, error) {
	if !pinnedUnderProxyPins(path) {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"pinned certificate %q is outside %s",
			path, ProxyPins)
	}
	read, err := h.run(ctx, "read the certificate pinned at "+PinCertificate(path),
		"cat "+quoted(PinCertificate(path)), nil)
	if err != nil {
		return nil, err
	}
	return []byte(read), nil
}

func (h *Host) CertificateTrouble(ctx context.Context, hostname string) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	limit, said := certs.RateLimited(h.said(ctx, logCommand(ProxyContainer), elevation))
	if !said || !limit.Covers(hostname) || limit.Spent(time.Now()) {
		return nil
	}
	return limit.Refusal(hostname)
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
		report.Detail("Removed " + removed + ": certificate for a hostname no longer served")
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

type Answer struct {
	Edge      string
	Unreached string
}

func (h *Host) ServedEdge(ctx context.Context, hostname string) (Answer, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return Answer{}, err
	}
	result, err := h.stream(ctx, words(helperCommand("probe", hostname)), nil, elevation)
	if err != nil {
		return Answer{}, err
	}
	switch result.Code {
	case 0:
		return Answer{Edge: strings.TrimSpace(result.Stdout)}, nil
	case proxyServesNoCertificate:
		return Answer{Unreached: spoken(result)}, nil
	default:
		return Answer{}, h.refuse("probe "+hostname+" from inside the proxy", result)
	}
}
