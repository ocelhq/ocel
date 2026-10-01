package vps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

const servingPortTimeout = 3 * time.Second

const wsaConnectionRefused = syscall.Errno(10061)

var servingPorts = []string{proxy.HTTPPort, proxy.HTTPSPort}

type closedPort struct {
	port  string
	cause error
}

func (p *Provider) refuseServingPortsClosedFromOutside(ctx context.Context, tier environment.Tier) error {
	if err := p.host.ProxyOwnsServingPorts(ctx); err != nil {
		return err
	}
	said, err := p.readServingPortsClosedFromOutside(ctx)
	if err != nil || said == "" {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s\nEverything else the bootstrap installs is in place: run `%s` again once the ports are open.",
		said, provider.BootstrapCommand(tier))
}

func (p *Provider) readServingPortsClosedFromOutside(ctx context.Context) (string, error) {
	address, err := p.host.Address(ctx)
	if err != nil {
		return "", err
	}
	closed := findClosedPorts(ctx, p.reach(), address)
	if len(closed) == 0 {
		return "", nil
	}
	return describeClosedPorts(address, closed), nil
}

func findClosedPorts(ctx context.Context, reach Reach, address string) []closedPort {
	causes := make([]error, len(servingPorts))
	var probes sync.WaitGroup
	for at, port := range servingPorts {
		probes.Go(func() {
			probing, stop := context.WithTimeout(ctx, servingPortTimeout)
			defer stop()
			causes[at] = reach(probing, net.JoinHostPort(address, port))
		})
	}
	probes.Wait()
	var closed []closedPort
	for at, cause := range causes {
		if cause != nil {
			closed = append(closed, closedPort{port: servingPorts[at], cause: cause})
		}
	}
	return closed
}

func describeClosedPorts(address string, closed []closedPort) string {
	ports := make([]string, 0, len(closed))
	lines := make([]string, 0, len(closed))
	for _, one := range closed {
		ports = append(ports, one.port)
		lines = append(lines, fmt.Sprintf("  port %s: %s", one.port, explainClosed(one.cause)))
	}
	named, verb, them := "port "+ports[0], "accepts", "it"
	if len(ports) > 1 {
		named, verb, them = "ports "+strings.Join(ports, " and "), "accept", "them"
	}
	return fmt.Sprintf("%s on %s %s no connection from this machine, though the proxy on the box listens on %s:\n%s\n"+
		"A cloud firewall in front of the box most likely blocks %s: allow inbound TCP %s from anywhere in the GCP VPC firewall rules, the AWS security group, or your provider's equivalent. "+
		"Until then nothing outside reaches the hostnames this box serves, and its proxy cannot obtain certificates for them.",
		named, address, verb, them, strings.Join(lines, "\n"), them, strings.Join(ports, " and "))
}

func explainClosed(cause error) string {
	var timing interface{ Timeout() bool }
	switch {
	case errors.Is(cause, syscall.ECONNREFUSED), errors.Is(cause, wsaConnectionRefused):
		return "refused, so a firewall on the way or on the box rejects it before the proxy sees it"
	case errors.Is(cause, os.ErrDeadlineExceeded), errors.Is(cause, context.DeadlineExceeded),
		errors.Is(cause, syscall.ETIMEDOUT), errors.As(cause, &timing) && timing.Timeout():
		return fmt.Sprintf("timed out after %s, so something between this machine and the box drops it", servingPortTimeout)
	default:
		return cause.Error()
	}
}
