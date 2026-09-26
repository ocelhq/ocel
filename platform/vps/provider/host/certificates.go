package host

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const proxyNotServingYet = 3

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
	leaf, err := certs.Parse(caddy.PinCertificate(pin.Path), block)
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
	certificate, key := quoted(caddy.PinCertificate(path)), quoted(caddy.PinKey(path))
	return "if ! command -v openssl >/dev/null 2>&1; then echo " + pairUnchecked + "; exit 0; fi\n" +
		"held=$(openssl x509 -in " + certificate + " -noout -pubkey 2>/dev/null | openssl pkey -pubin -outform DER 2>/dev/null | sha256sum)\n" +
		"keyed=$(openssl pkey -in " + key + " -pubout -outform DER 2>/dev/null | sha256sum)\n" +
		"if [ -n \"$held\" ] && [ \"$held\" = \"$keyed\" ]; then echo " + pairMatched + "; else echo " + pairMismatched + "; fi"
}

func (h *Host) paired(ctx context.Context, pin Pin) error {
	said, err := h.run(ctx, "check the key pinned beside "+caddy.PinCertificate(pin.Path), pairCommand(pin.Path), nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(said) == pairMismatched {
		return refusal.Refuse(refusal.CodeInvalid,
			"the key at %s does not match the certificate at %s",
			caddy.PinKey(pin.Path), caddy.PinCertificate(pin.Path))
	}
	return nil
}

func (h *Host) PinnedCertificate(ctx context.Context, path string) ([]byte, error) {
	if _, pinned := caddy.Pinned(path); !pinned {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"pinned certificate %q is outside %s",
			path, caddy.PinsDir)
	}
	read, err := h.run(ctx, "read the certificate pinned at "+caddy.PinCertificate(path),
		"cat "+quoted(caddy.PinCertificate(path)), nil)
	if err != nil {
		return nil, err
	}
	return []byte(read), nil
}

func (h *Host) FrontProxy() proxy.Proxy { return h.front }

func (h *Host) ServedCertificate(ctx context.Context, hostname string) ([]byte, error) {
	result, err := h.stream(ctx, words([]string{SwitchboardBinary, "leaf", hostname}), nil, "")
	if err != nil {
		return nil, err
	}
	switch result.Code {
	case 0:
		return []byte(result.Stdout), nil
	case proxyNotServingYet:
		return nil, nil
	default:
		return nil, h.refuse("read what this box serves for "+hostname, result, "")
	}
}

type Answer struct {
	Edge      string
	Unreached string
}

func (h *Host) ServedEdge(ctx context.Context, hostname string) (Answer, error) {
	result, err := h.stream(ctx, words([]string{SwitchboardBinary, "probe", hostname}), nil, "")
	if err != nil {
		return Answer{}, err
	}
	switch result.Code {
	case 0:
		return Answer{Edge: strings.TrimSpace(result.Stdout)}, nil
	case proxyNotServingYet:
		return Answer{Unreached: spoken(result)}, nil
	default:
		return Answer{}, h.refuse("probe "+hostname+" on this box's own https port", result, "")
	}
}

type frontBox struct{ h *Host }

func (b frontBox) Ran(ctx context.Context, what string, argv []string) (string, error) {
	elevation, err := b.h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	return b.h.ran(ctx, what, words(argv), nil, elevation)
}

func (b frontBox) Listening(ctx context.Context) ([]listeners.Listener, error) {
	return b.h.Listening(ctx)
}

func (b frontBox) Publishing(ctx context.Context, port string) ([]string, error) {
	return b.h.Publishing(ctx, port)
}

func (b frontBox) Claimed(ctx context.Context) ([]string, error) {
	state, err := b.h.routingTable(ctx)
	if err != nil {
		return nil, err
	}
	return state.hostnames(), nil
}

func (b frontBox) Probe(ctx context.Context, hostname string) (string, string, error) {
	said, err := b.h.ServedEdge(ctx, hostname)
	return said.Edge, said.Unreached, err
}

func (b frontBox) Said(ctx context.Context, argv []string) (string, error) {
	elevation, err := b.h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	return b.h.said(ctx, words(argv), elevation), nil
}
