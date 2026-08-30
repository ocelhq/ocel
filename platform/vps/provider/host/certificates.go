package host

import (
	"context"
)

const proxyServesNoCertificate = 3

func (h *Host) PinnedCertificate(ctx context.Context, path string) ([]byte, error) {
	read, err := h.run(ctx, "read the certificate pinned at "+PinCertificate(path),
		"cat "+quoted(PinCertificate(path)), nil)
	if err != nil {
		return nil, err
	}
	return []byte(read), nil
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
