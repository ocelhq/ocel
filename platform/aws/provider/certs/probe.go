package certs

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Prober struct {
	Get      func(ctx context.Context, url string) (http.Header, error)
	Wait     func(context.Context, time.Duration) error
	Attempts int
	Every    time.Duration
	Now      func() time.Time
	Jitter   func() float64
}

const (
	probeAttempts = 30
	probeEvery    = 10 * time.Second
	probeTimeout  = 15 * time.Second
	probeJitter   = 0.05
)

func NewProber() Prober {
	return Prober{Get: httpGet, Wait: waitFor, Attempts: probeAttempts, Every: probeEvery, Now: time.Now, Jitter: rand.Float64}
}

func httpGet(ctx context.Context, url string) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	return resp.Header, nil
}

func (p Prober) attempts() int {
	return max(p.Attempts, 1)
}

func (p Prober) every() time.Duration {
	if p.Every <= 0 {
		return probeEvery
	}
	return p.Every
}

func (p Prober) interval() time.Duration {
	every := p.every()
	spread := probeJitter * (2*p.jitter() - 1)
	return every + time.Duration(float64(every)*spread)
}

func (p Prober) jitter() float64 {
	if p.Jitter == nil {
		return rand.Float64()
	}
	return p.Jitter()
}

func (p Prober) window() time.Duration {
	return time.Duration(p.attempts()-1) * p.every()
}

func (p Prober) hold(ctx context.Context) error {
	if p.Wait != nil {
		return p.Wait(ctx, p.interval())
	}
	return waitFor(ctx, p.interval())
}

func (p Prober) Clock() time.Time {
	return p.now()
}

func (p Prober) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

func (p Prober) Await(ctx context.Context, hostname string, kind edge.Kind, owed []edge.Record, say func(string)) (Probe, error) {
	target := probeURL(hostname)
	say(fmt.Sprintf("Checking %s answers as the %s edge, for up to %s", target, kind, p.window()))

	var seen string
	var lastErr error
	for attempt := range p.attempts() {
		if attempt > 0 {
			if err := p.hold(ctx); err != nil {
				return Probe{}, err
			}
		}
		header, err := p.Get(ctx, target)
		if err != nil {
			lastErr = err
			continue
		}
		lastErr = nil
		seen = header.Get(edge.HeaderEdge)
		if edge.ServedBy(seen, kind) {
			say(fmt.Sprintf("%s answers %s: %s", target, edge.HeaderEdge, kind))
			return Probe{At: p.now(), Edge: kind, OK: true}, nil
		}
	}
	return Probe{At: p.now(), Edge: kind}, fmt.Errorf(
		"gave up after %s waiting for %s to answer as the %s edge: %s%s — re-run once it does, this picks up where it left off",
		p.window(), target, kind, probeSymptom(seen, lastErr), stillOwed(owed),
	)
}

func stillOwed(records []edge.Record) string {
	if len(records) == 0 {
		return ""
	}
	return "; still outstanding: " + outstanding(records)
}

func probeSymptom(seen string, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("the request never completed (%v)", err)
	case seen == "":
		return fmt.Sprintf("it answers without an %s header, so something other than ocel is serving it", edge.HeaderEdge)
	default:
		return fmt.Sprintf("it answers %s: %s", edge.HeaderEdge, seen)
	}
}

func probeURL(hostname string) string {
	return "https://" + edge.ProbeHostname(hostname) + "/"
}
