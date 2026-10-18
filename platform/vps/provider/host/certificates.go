package host

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const proxyNotServingYet = 3

func (h *Host) FrontProxy() proxy.Proxy { return h.front }

func (h *Host) ReadServedCertificate(ctx context.Context, hostname string) ([]byte, error) {
	result, err := h.Stream(ctx, ssh.Login, shell.Words([]string{boxapi.SwitchboardBinary, "leaf", hostname}), nil)
	if err != nil {
		return nil, err
	}
	switch result.Code {
	case 0:
		return []byte(result.Stdout), nil
	case proxyNotServingYet:
		return nil, nil
	default:
		return nil, h.Refuse("read what this box serves for "+hostname, result)
	}
}

type Answer struct {
	Router  router.Kind
	Failure string
}

func (h *Host) ProbeRouter(ctx context.Context, hostname string) (Answer, error) {
	return h.probe(ctx, "probe "+hostname+" on this box's own https port", hostname)
}

func (h *Host) ProbeRouterOverPlainHTTP(ctx context.Context, hostname string) (Answer, error) {
	return h.probe(ctx, "probe "+hostname+" on this box's own http port", hostname, "--plain")
}

func (h *Host) probe(ctx context.Context, what, hostname string, flags ...string) (Answer, error) {
	result, err := h.Stream(ctx, ssh.Login, shell.Words(slices.Concat([]string{boxapi.SwitchboardBinary, "probe"}, flags, []string{hostname})), nil)
	if err != nil {
		return Answer{}, err
	}
	switch result.Code {
	case 0:
		return Answer{Router: router.Kind(strings.TrimSpace(result.Stdout))}, nil
	case proxyNotServingYet:
		return Answer{Failure: ssh.Reason(result)}, nil
	default:
		return Answer{}, h.Refuse(what, result)
	}
}

type frontBox struct{ h *Host }

func (b frontBox) Ran(ctx context.Context, what string, argv []string) (string, error) {
	return b.RanWithStdin(ctx, what, argv, nil)
}

func (b frontBox) RanWithStdin(ctx context.Context, what string, argv []string, stdin []byte) (string, error) {
	elevation, err := b.elevation(ctx, argv)
	if err != nil {
		return "", err
	}
	var fed io.Reader
	if stdin != nil {
		fed = bytes.NewReader(stdin)
	}
	return b.h.Run(ctx, elevation, what, shell.Words(argv), fed)
}

func (b frontBox) elevation(ctx context.Context, argv []string) (ssh.Acting, error) {
	if len(argv) == 0 || argv[0] != "docker" && argv[0] != "sh" {
		return ssh.Login, nil
	}
	return ssh.Docker, b.h.Assume(ctx, ssh.Docker)
}

func (b frontBox) PlacedSum(ctx context.Context, path string) (string, error) {
	return b.h.routing.DestinationSum(ctx, path)
}

func (b frontBox) Listening(ctx context.Context) ([]listeners.Listener, error) {
	return b.h.Listening(ctx)
}

func (b frontBox) Publishing(ctx context.Context, port string) ([]string, error) {
	return b.h.Publishing(ctx, port)
}

func (b frontBox) Claimed(ctx context.Context) ([]string, error) {
	state, err := b.h.routing.Read(ctx)
	if err != nil {
		return nil, err
	}
	return state.Hostnames(), nil
}

func (b frontBox) Shielded(ctx context.Context) ([]string, error) {
	state, err := b.h.routing.Read(ctx)
	if err != nil {
		return nil, err
	}
	shielded := make([]string, 0, len(state.Shields))
	for _, shield := range state.Shields {
		shielded = append(shielded, shield.Hostname)
	}
	return shielded, nil
}

func (h *Host) RefuseUnshielded(ctx context.Context, hostname string) error {
	return h.front.RefuseUnshielded(ctx, hostname)
}

func (b frontBox) Probe(ctx context.Context, hostname string) (router.Kind, string, error) {
	said, err := b.h.ProbeRouter(ctx, hostname)
	return said.Router, said.Failure, err
}

func (b frontBox) ReadSpec(ctx context.Context) (proxy.Spec, error) {
	state, err := b.h.routing.Read(ctx)
	if err != nil {
		return proxy.Spec{}, err
	}
	return routing.Spec(state), nil
}

func (b frontBox) ProbeAnyCertificate(ctx context.Context, hostname string) (string, string, error) {
	said, err := b.h.probeAnyCertificate(ctx, hostname)
	return string(said.Router), said.Failure, err
}

func (b frontBox) Pause(ctx context.Context, wait time.Duration) error { return b.h.Wait(ctx, wait) }

func (b frontBox) ReadLeaf(ctx context.Context, hostname string) ([]byte, error) {
	return b.h.ReadServedCertificate(ctx, hostname)
}

func (b frontBox) ReadBeside(ctx context.Context, path string) ([]boxapi.SiblingFile, error) {
	elevation, err := ssh.Docker, b.h.Assume(ctx, ssh.Docker)
	if err != nil {
		return nil, err
	}
	said, err := b.h.Run(ctx, elevation, "read what your proxy reads beside "+path, shell.Words(routing.Placement(filepath.Dir(path), "beside", path)), nil)
	if err != nil {
		return nil, err
	}
	var beside []boxapi.SiblingFile
	if err := json.Unmarshal([]byte(said), &beside); err != nil {
		return nil, unread("what was read beside "+path, said)
	}
	return beside, nil
}

func (b frontBox) ProbePlainHTTP(ctx context.Context, hostname string) (router.Kind, string, error) {
	said, err := b.h.ProbeRouterOverPlainHTTP(ctx, hostname)
	return said.Router, said.Failure, err
}

func (b frontBox) Said(ctx context.Context, argv []string) (string, error) {
	elevation, err := ssh.Docker, b.h.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", err
	}
	return b.h.Said(ctx, elevation, shell.Words(argv))
}
