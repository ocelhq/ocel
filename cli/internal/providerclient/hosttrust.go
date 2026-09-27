package providerclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Confirmer interface {
	Attended() bool
	Confirm(ctx context.Context, question string) (bool, error)
}

type Trust struct {
	Ask  Confirmer
	Out  io.Writer
	Hold func(waiting *streamv1.WaitingEvent) (resume func(reason string))
}

func (t Trust) attended() bool { return t.Ask != nil && t.Out != nil && t.Ask.Attended() }

func (t Trust) hold() func(reason string) {
	if t.Hold == nil {
		return func(string) {}
	}
	return t.Hold(&streamv1.WaitingEvent{})
}

func (t Trust) acceptKey(ctx context.Context, err error) (bool, error) {
	refusal, ok := provider.HostTrustOf(err)
	if !ok || refusal.Terminal() || refusal.Reason != provider.UnknownHostKey || !t.attended() {
		return false, err
	}

	offered, keyErr := refusal.Got.Fingerprinted()
	if keyErr != nil {
		return false, errors.Join(err, keyErr)
	}
	entry := refusal.KnownHostsEntry()
	if !provider.ValidKnownHostsEntry(entry) {
		return false, errors.Join(err, fmt.Errorf("the provider named %q, which is not a name a known_hosts entry can be keyed on", entry))
	}
	store, storeErr := knownHostsStore(refusal)
	if storeErr != nil {
		return false, errors.Join(err, storeErr)
	}

	refusal.Got = offered
	if len(refusal.KnownHosts) == 0 {
		refusal.KnownHosts = []string{store}
	}
	accepted, askErr := t.ask(ctx, refusal, entry, store)
	if askErr != nil {
		return false, errors.Join(err, askErr)
	}
	if !accepted {
		return false, err
	}

	if recordErr := record(store, entry+" "+offered.Type+" "+offered.Key+"\n"); recordErr != nil {
		return false, errors.Join(err, fmt.Errorf("record the host key in %s: %w", store, recordErr))
	}
	return true, nil
}

func (t Trust) ask(ctx context.Context, refusal provider.HostTrust, entry, store string) (bool, error) {
	resume := t.hold()
	defer resume("answered")

	fmt.Fprintln(t.Out, refusal.Offer())
	return t.Ask.Confirm(ctx, fmt.Sprintf("Trust that key and record %s in %s?", entry, store))
}

func knownHostsStore(trust provider.HostTrust) (string, error) {
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
	known, err := os.ReadFile(store)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if hasLine(string(known), line) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(store, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	if len(known) > 0 && !strings.HasSuffix(string(known), "\n") {
		line = "\n" + line
	}
	if _, err := file.WriteString(line); err != nil {
		return err
	}
	return file.Sync()
}

func hasLine(content, line string) bool {
	want := strings.Fields(line)
	if len(want) != 3 {
		return false
	}
	for _, existing := range strings.Split(content, "\n") {
		fields := strings.Fields(existing)
		if len(fields) != 3 || fields[1] != want[1] || fields[2] != want[2] {
			continue
		}
		for _, named := range strings.Split(fields[0], ",") {
			if named == want[0] {
				return true
			}
		}
	}
	return false
}
