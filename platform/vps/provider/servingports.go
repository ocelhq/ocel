package vps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
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
	return p.askServingPortsOpened(ctx, tier)
}

func (p *Provider) askServingPortsOpened(ctx context.Context, tier environment.Tier) error {
	address, err := p.host.Address(ctx)
	if err != nil {
		return err
	}
	closed := findClosedPorts(ctx, p.reach(), address)
	if len(closed) == 0 {
		return nil
	}
	said := describeClosedPorts(address, closed)
	return provider.Ask(
		fmt.Sprintf("%s\nRun `%s` again once you have opened %s.", said, provider.BootstrapCommand(tier), namePorts(closed)),
		provider.Question{
			Finding: said,
			Prompt:  fmt.Sprintf("Have you opened %s?", namePorts(closed)),
			Confirm: func(ctx context.Context) error { return p.askServingPortsOpened(ctx, tier) },
		})
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
	numbers := make([]string, 0, len(closed))
	reasons := make([]string, 0, len(closed))
	refused := false
	for _, one := range closed {
		numbers = append(numbers, one.port)
		reasons = append(reasons, one.port+" "+explainClosed(one.cause))
		refused = refused || isRefused(one.cause)
	}
	why := strings.Join(reasons, ", ")
	if same := explainClosed(closed[0].cause); !slices.ContainsFunc(closed, func(one closedPort) bool { return explainClosed(one.cause) != same }) {
		why = same
	}
	verb := "is"
	if len(closed) > 1 {
		verb = "are"
	}
	where := "your cloud firewall (GCP VPC firewall rules, AWS security group, or equivalent)"
	if refused {
		where += " or the box's own firewall"
	}
	return fmt.Sprintf("%s on %s %s unreachable from this machine (%s).\nAllow inbound TCP %s in %s.",
		namePorts(closed), address, verb, why,
		strings.Join(numbers, " and "), where)
}

func namePorts(closed []closedPort) string {
	numbers := make([]string, 0, len(closed))
	for _, one := range closed {
		numbers = append(numbers, one.port)
	}
	if len(numbers) == 1 {
		return "port " + numbers[0]
	}
	return "ports " + strings.Join(numbers, " and ")
}

func isRefused(cause error) bool {
	return errors.Is(cause, syscall.ECONNREFUSED) || errors.Is(cause, wsaConnectionRefused)
}

func explainClosed(cause error) string {
	var timing interface{ Timeout() bool }
	switch {
	case isRefused(cause):
		return "refused"
	case errors.Is(cause, os.ErrDeadlineExceeded), errors.Is(cause, context.DeadlineExceeded),
		errors.Is(cause, syscall.ETIMEDOUT), errors.As(cause, &timing) && timing.Timeout():
		return "timed out"
	default:
		return cause.Error()
	}
}
