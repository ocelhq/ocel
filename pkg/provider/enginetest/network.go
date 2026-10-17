package enginetest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	slotLabel = "ocel.test.slot"
	slotCount = 6

	InternalBridge = "com.docker.network.bridge.inhibit_ipv4=true"
)

type slotLease struct {
	once    sync.Once
	slot    int
	held    *os.File
	refused string
}

var lease slotLease

func Network(t *testing.T, role string) string {
	t.Helper()
	return leasedNetwork(t, role)
}

func InternalNetwork(t *testing.T, role string) string {
	t.Helper()
	return leasedNetwork(t, role+"-internal", "--opt", InternalBridge)
}

func leasedNetwork(t *testing.T, role string, options ...string) string {
	t.Helper()
	requireDocker(t)
	lease.once.Do(func() {
		dir, err := slotDir()
		if err != nil {
			lease.refused = fmt.Sprintf("no directory to hold this run's network slot in: %v", err)
			return
		}
		lease.slot, lease.held, err = leaseSlot(dir, slotCount)
		if err != nil {
			lease.refused = fmt.Sprintf("lease a network slot: %v", err)
			return
		}
		if err := emptySlot(lease.slot); err != nil {
			lease.refused = fmt.Sprintf("empty the networks of slot %d before this run uses them: %v", lease.slot, err)
		}
	})
	if lease.refused != "" {
		t.Skip(lease.refused)
	}
	name, err := slotNetwork(lease.slot, role, options...)
	if err != nil {
		t.Skipf("this machine's engine will not provide the %s network the run's containers resolve each other across: %v", role, err)
	}
	return name
}

func slotDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "ocel", "test-network-slots")
	return dir, os.MkdirAll(dir, 0o755)
}

func slotNetworkName(slot int, role string) string {
	return rootPrefix + strconv.Itoa(slot) + "-" + role
}

func slotNetwork(slot int, role string, options ...string) (string, error) {
	name := slotNetworkName(slot, role)
	if exec.Command(engine, "network", "inspect", name).Run() == nil {
		return name, nil
	}
	argv := append(append([]string{"network", "create", "--label", slotLabel + "=" + strconv.Itoa(slot)}, options...), name)
	said, err := exec.Command(engine, argv...).CombinedOutput()
	if err != nil && exec.Command(engine, "network", "inspect", name).Run() != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(said)))
	}
	return name, nil
}

func emptySlot(slot int) error {
	prefix := slotNetworkName(slot, "")
	said, err := exec.Command(engine, "network", "ls", "--filter", "name="+prefix, "--format", "{{.Name}}").Output()
	if err != nil {
		return fmt.Errorf("list the networks of slot %d: %w", slot, err)
	}
	var failed []error
	for _, network := range strings.Fields(string(said)) {
		if strings.HasPrefix(network, prefix) {
			failed = append(failed, emptyNetwork(network))
		}
	}
	return errors.Join(failed...)
}

func emptyNetwork(network string) error {
	said, err := exec.Command(engine, "network", "inspect", "--format", `{{range .Containers}}{{.Name}} {{end}}`, network).Output()
	if err != nil {
		return fmt.Errorf("read what is attached to network %s: %w", network, err)
	}
	if attached := strings.Fields(string(said)); len(attached) > 0 {
		return docker(append([]string{"rm", "--force", "--volumes"}, attached...)...)
	}
	return nil
}
