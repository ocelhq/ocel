package certs

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Pins struct {
	box  *ssh.Box
	pins []boxapi.Pin

	verifying sync.Mutex
	verified  bool
	unusable  error
}

func NewPins(box *ssh.Box, pins []boxapi.Pin) *Pins {
	return &Pins{box: box, pins: pins}
}

func (p *Pins) List() []boxapi.Pin { return slices.Clone(p.pins) }

func (p *Pins) Covering(hostname string) string { return Covering(p.pins, hostname) }

func (p *Pins) Verified(ctx context.Context) ([]boxapi.Pin, error) {
	p.verifying.Lock()
	defer p.verifying.Unlock()
	if p.verified {
		return slices.Clone(p.pins), p.unusable
	}
	for _, pin := range p.pins {
		if err := p.verify(ctx, pin); err != nil {
			return nil, err
		}
	}
	p.verified = true
	return slices.Clone(p.pins), nil
}

func (p *Pins) verify(ctx context.Context, pin boxapi.Pin) error {
	refused := func(err error) error {
		p.verified, p.unusable = true, err
		return err
	}
	if err := RefuseMisplacedPin(pin); err != nil {
		return refused(err)
	}
	block, err := p.Certificate(ctx, pin.Path)
	if err != nil {
		return err
	}
	leaf, err := Parse(boxapi.PinCertificate(pin.Path), block)
	if err != nil {
		return refused(err)
	}
	if err := Verify(pin.Path, pin.Hostname, leaf, time.Now()); err != nil {
		return refused(err)
	}
	if err := p.refuseUnpaired(ctx, pin); err != nil {
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
	certificate, key := shell.Quote(boxapi.PinCertificate(path)), shell.Quote(boxapi.PinKey(path))
	return "if ! command -v openssl >/dev/null 2>&1; then echo " + pairUnchecked + "; exit 0; fi\n" +
		"certified=$(openssl x509 -in " + certificate + " -noout -pubkey 2>/dev/null | openssl pkey -pubin -outform DER 2>/dev/null | sha256sum)\n" +
		"keyed=$(openssl pkey -in " + key + " -pubout -outform DER 2>/dev/null | sha256sum)\n" +
		"if [ -n \"$certified\" ] && [ \"$certified\" = \"$keyed\" ]; then echo " + pairMatched + "; else echo " + pairMismatched + "; fi"
}

func (p *Pins) refuseUnpaired(ctx context.Context, pin boxapi.Pin) error {
	said, err := p.box.Run(ctx, ssh.Root, "check the key pinned beside "+boxapi.PinCertificate(pin.Path), pairCommand(pin.Path), nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(said) == pairMismatched {
		return refusal.Refuse(refusal.CodeInvalid,
			"the key at %s does not match the certificate at %s",
			boxapi.PinKey(pin.Path), boxapi.PinCertificate(pin.Path))
	}
	return nil
}

func (p *Pins) Certificate(ctx context.Context, path string) ([]byte, error) {
	if _, pinned := boxapi.Pinned(path); !pinned {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"pinned certificate %q is outside %s",
			path, boxapi.PinsDir)
	}
	read, err := p.box.Run(ctx, ssh.Root, "read the certificate pinned at "+boxapi.PinCertificate(path), "cat "+shell.Quote(boxapi.PinCertificate(path)), nil)
	if err != nil {
		return nil, err
	}
	return []byte(read), nil
}

func pinCovers(pin boxapi.Pin, hostname string) bool {
	return (Leaf{Domains: []string{pin.Hostname}}).Covers(hostname)
}

func Covering(pins []boxapi.Pin, hostname string) string {
	for _, exact := range []bool{true, false} {
		at := slices.IndexFunc(pins, func(pin boxapi.Pin) bool {
			return exact == (pin.Hostname == hostname) && pinCovers(pin, hostname) && RefuseMisplacedPin(pin) == nil
		})
		if at >= 0 {
			return pins[at].Path
		}
	}
	return ""
}

func RefuseMisplacedPin(pin boxapi.Pin) error {
	if _, pinned := boxapi.Pinned(pin.Path); pin.Hostname == "" || !pinned {
		return refusal.Refuse(refusal.CodeInvalid,
			"pinned certificate for %q is at %q, not under %s/",
			pin.Hostname, pin.Path, boxapi.PinsDir)
	}
	return nil
}
