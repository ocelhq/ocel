package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Confirmer interface {
	Interactive() bool
	Confirm(ctx context.Context, question string) (bool, error)
}

type Trust struct {
	Ask     Confirmer
	Out     io.Writer
	Suspend func() func()
}

func (t Trust) interactive() bool { return t.Ask != nil && t.Out != nil && t.Ask.Interactive() }

func (t Trust) suspend() func() {
	if t.Suspend == nil {
		return func() {}
	}
	return t.Suspend()
}

func driveTrusting(ctx context.Context, trust Trust, drive func() error) error {
	err := drive()

	refusal, ok := providerkit.HostTrustOf(err)
	if !ok || refusal.Terminal() || refusal.Reason != providerkit.UnknownHostKey || !trust.interactive() {
		return err
	}

	offered, keyErr := refusal.Got.Fingerprinted()
	if keyErr != nil {
		return errors.Join(err, keyErr)
	}
	entry := refusal.KnownHostsEntry()
	if !providerkit.ValidKnownHostsEntry(entry) {
		return errors.Join(err, fmt.Errorf("the provider named %q, which is not a name a known_hosts entry can be keyed on", entry))
	}
	store, storeErr := knownHostsStore(refusal)
	if storeErr != nil {
		return errors.Join(err, storeErr)
	}

	refusal.Got = offered
	if len(refusal.KnownHosts) == 0 {
		refusal.KnownHosts = []string{store}
	}
	accepted, askErr := ask(ctx, trust, refusal, entry, store)
	if askErr != nil {
		return errors.Join(err, askErr)
	}
	if !accepted {
		return err
	}

	if recordErr := record(store, entry+" "+offered.Type+" "+offered.Key+"\n"); recordErr != nil {
		return errors.Join(err, fmt.Errorf("record the host key in %s: %w", store, recordErr))
	}
	return drive()
}

func ask(ctx context.Context, trust Trust, refusal providerkit.HostTrust, entry, store string) (bool, error) {
	resume := trust.suspend()
	defer resume()

	fmt.Fprintln(trust.Out, refusal.Offer())
	return trust.Ask.Confirm(ctx, fmt.Sprintf("Trust that key and record %s in %s?", entry, store))
}

func knownHostsStore(trust providerkit.HostTrust) (string, error) {
	if len(trust.KnownHosts) > 0 && trust.KnownHosts[0] != "" {
		return writable(trust.KnownHosts[0])
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return writable(filepath.Join(home, ".ssh", "known_hosts"))
}

func writable(store string) (string, error) {
	info, err := os.Stat(store)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file, so a host key recorded there would be thrown away", store)
	}
	return store, nil
}

func record(store, line string) error {
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(store, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		line = "\n" + line
	}
	if _, err := file.WriteString(line); err != nil {
		return err
	}
	return file.Sync()
}
