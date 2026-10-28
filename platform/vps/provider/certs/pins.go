package certs

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
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
	pair, err := p.read(ctx, pin.Path)
	if err != nil {
		return err
	}
	leaf, err := Parse(boxapi.PinCertificate(pin.Path), []byte(pair.Certificate))
	if err != nil {
		return refused(err)
	}
	if err := Verify(pin.Path, pin.Hostname, leaf, time.Now()); err != nil {
		return refused(err)
	}
	if !pair.KeyMatches {
		return refused(refusal.Refuse(refusal.CodeInvalid,
			"the key at %s does not match the certificate at %s",
			boxapi.PinKey(pin.Path), boxapi.PinCertificate(pin.Path)))
	}
	return nil
}

func (p *Pins) Certificate(ctx context.Context, path string) ([]byte, error) {
	pair, err := p.read(ctx, path)
	if err != nil {
		return nil, err
	}
	return []byte(pair.Certificate), nil
}

func (p *Pins) read(ctx context.Context, path string) (boxapi.PinnedPair, error) {
	leaf, pinned := boxapi.Pinned(path)
	if !pinned {
		return boxapi.PinnedPair{}, refusal.Refuse(refusal.CodeInvalid,
			"pinned certificate %q is outside %s",
			path, boxapi.PinsDir)
	}
	said, err := p.box.RunLive(ctx, ssh.Granted, "read the certificate pinned at "+boxapi.PinCertificate(path), ssh.LiveCommand{Fixed: []string{boxapi.LiveBinary, boxapi.PinCommand}, Rest: []string{leaf}}, nil)
	if err != nil {
		return boxapi.PinnedPair{}, err
	}
	var pair boxapi.PinnedPair
	if err := json.Unmarshal([]byte(said), &pair); err != nil {
		return boxapi.PinnedPair{}, refusal.Refuse(refusal.CodeDenied,
			"the box answered a read of %s with %d unreadable bytes", boxapi.PinCertificate(path), len(said))
	}
	return pair, nil
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
