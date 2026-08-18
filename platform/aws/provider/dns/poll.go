package dns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Lookup func(ctx context.Context, hostname string) ([]string, error)

type Poller struct {
	Lookup   Lookup
	Wait     func(context.Context, time.Duration) error
	Attempts int
	Every    time.Duration
}

const (
	pollAttempts = 60
	pollEvery    = 5 * time.Second
)

func NewPoller() Poller {
	return Poller{
		Lookup: func(ctx context.Context, hostname string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, hostname)
		},
		Wait:     waitFor,
		Attempts: pollAttempts,
		Every:    pollEvery,
	}
}

func waitFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p Poller) Await(ctx context.Context, records []edge.Record, say func(string)) error {
	if len(records) == 0 {
		return nil
	}
	for _, rec := range records {
		say(fmt.Sprintf(
			"Nothing writes DNS for this project — %s, and this waits up to %s for it to resolve",
			rec.Instruction(), p.window(),
		))
	}

	pending := records
	for attempt := 0; attempt < max(p.Attempts, 1); attempt++ {
		if attempt > 0 {
			if err := p.Wait(ctx, p.Every); err != nil {
				return err
			}
		}
		var missing []edge.Record
		for _, rec := range pending {
			live, err := p.Lookup(ctx, probeHostname(rec.Name))
			if err != nil || len(live) == 0 {
				missing = append(missing, rec)
				continue
			}
			say(fmt.Sprintf("%s resolves", rec.Name))
		}
		if len(missing) == 0 {
			return nil
		}
		pending = missing
	}

	wanted := make([]string, 0, len(pending))
	for _, rec := range pending {
		wanted = append(wanted, rec.Instruction())
	}
	return fmt.Errorf(
		"gave up after %s waiting for DNS: %s, then re-run — or declare `dns` (`route53()` or `cloudflareDns()`) and ocel writes the record itself",
		p.window(), strings.Join(wanted, "; "),
	)
}

func (p Poller) window() time.Duration {
	return time.Duration(max(p.Attempts, 1)-1) * p.Every
}

func Release(ctx context.Context, writer edge.DNSWriter, records []edge.Record, say func(string)) error {
	if len(records) == 0 {
		return nil
	}
	if writer == nil {
		for _, rec := range records {
			say(fmt.Sprintf("Leaving %s standing: nothing here writes DNS any more — delete it by hand, or declare `dns` and run this again", rec))
		}
		return nil
	}
	for _, rec := range records {
		say(fmt.Sprintf("Removing %s", rec))
	}
	return writer.DeleteRecords(ctx, records)
}

func probeHostname(name string) string {
	return strings.Replace(name, "*.", "ocel-dns-probe.", 1)
}
