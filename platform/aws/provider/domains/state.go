package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type StackRecord struct {
	providerkit.EdgeStackState
	Certificate certs.Certificate `json:"certificate,omitzero"`
	Validation  Records           `json:"validation,omitzero"`
}

func (r StackRecord) Empty() bool {
	return r.Edge.Empty() && r.Settlement().Empty()
}

func (r StackRecord) Settlement() Settlement {
	settled := Settlement{Certificate: r.Certificate, Validation: r.Validation}
	for _, hostname := range r.Hostnames() {
		settled.Hosts = append(settled.Hosts, hostOf(hostname, r.Host(hostname)))
	}
	return settled
}

func (r StackRecord) With(settled Settlement) StackRecord {
	next := StackRecord{
		EdgeStackState: providerkit.EdgeStackState{Edge: r.Edge},
		Certificate:    settled.Certificate,
		Validation:     settled.Validation,
	}
	for _, host := range settled.Hosts {
		next.Settle(host.Hostname, settledOf(host))
	}
	return next
}

type PreviewWildcard struct {
	providerkit.Wildcard
	Certificate certs.Certificate `json:"certificate,omitzero"`
	Validation  Records           `json:"validation,omitzero"`
}

func (w PreviewWildcard) Settlement() Settlement {
	return Settlement{
		Certificate: w.Certificate,
		Validation:  w.Validation,
		Hosts:       []Host{hostOf(w.Hostname(), w.Settled)},
	}
}

func (w PreviewWildcard) With(settled Settlement) PreviewWildcard {
	next := w
	next.Certificate, next.Validation = settled.Certificate, settled.Validation
	next.Settled = settledOf(settled.Host(w.Hostname()))
	return next
}

func hostOf(hostname string, settled providerkit.Settled) Host {
	return Host{
		Hostname:    hostname,
		Certificate: settled.Certificate,
		Records:     Records{Written: settled.Written, Owed: settled.Owed},
		Probe:       probeOf(settled.Probe),
	}
}

func settledOf(host Host) providerkit.Settled {
	return providerkit.Settled{
		Certificate: host.Certificate,
		Written:     host.Records.Written,
		Owed:        host.Records.Owed,
		Probe:       probeFrom(host.Probe),
	}
}

func probeOf(probe providerkit.Probe) certs.Probe {
	held := certs.Probe{OK: probe.OK, Edge: probe.Edge}
	if probe.At != 0 {
		held.At = time.Unix(probe.At, 0).UTC()
	}
	return held
}

func probeFrom(probe certs.Probe) providerkit.Probe {
	held := providerkit.Probe{OK: probe.OK, Edge: probe.Edge}
	if !probe.At.IsZero() {
		held.At = probe.At.Unix()
	}
	return held
}

type State struct {
	Records kit.RecordStore
}

func (s State) ReadStack(ctx context.Context, class edge.Class, slug string) (StackRecord, error) {
	var held StackRecord
	if err := s.read(ctx, providerkit.EdgeStackRecord(class, slug), &held); err != nil {
		return StackRecord{}, fmt.Errorf("read the edge-stack state for %s: %w", slug, err)
	}
	return held, nil
}

func (s State) WriteStack(ctx context.Context, class edge.Class, slug string, record StackRecord) error {
	if err := s.write(ctx, providerkit.EdgeStackRecord(class, slug), record); err != nil {
		return fmt.Errorf("record the edge-stack state for %s: %w", slug, err)
	}
	return nil
}

func (s State) ForgetStack(ctx context.Context, class edge.Class, slug string) error {
	if err := kit.Forget(ctx, s.Records, providerkit.EdgeStackRecord(class, slug)); err != nil {
		return fmt.Errorf("forget the edge-stack state for %s: %w", slug, err)
	}
	return nil
}

func (s State) StackSlugs(ctx context.Context, class edge.Class) ([]string, error) {
	held, err := s.Records.List(ctx, providerkit.EdgeStacksRecord(class))
	if err != nil {
		return nil, fmt.Errorf("list the projects with an edge stack: %w", err)
	}
	slugs := make([]string, 0, len(held))
	for _, record := range held {
		if slug := record.Name[len(record.Name)-1]; slug != "" {
			slugs = append(slugs, slug)
		}
	}
	slices.Sort(slugs)
	return slugs, nil
}

func (s State) ReadWildcard(ctx context.Context, class edge.Class) (PreviewWildcard, error) {
	var held PreviewWildcard
	if err := s.read(ctx, providerkit.WildcardRecord(class), &held); err != nil {
		return PreviewWildcard{}, fmt.Errorf("read the preview wildcard: %w", err)
	}
	return held, nil
}

func (s State) WriteWildcard(ctx context.Context, class edge.Class, wildcard PreviewWildcard) error {
	if err := s.write(ctx, providerkit.WildcardRecord(class), wildcard); err != nil {
		return fmt.Errorf("record the preview wildcard: %w", err)
	}
	return nil
}

func (s State) ForgetWildcard(ctx context.Context, class edge.Class) error {
	if err := kit.Forget(ctx, s.Records, providerkit.WildcardRecord(class)); err != nil {
		return fmt.Errorf("forget the preview wildcard: %w", err)
	}
	return nil
}

func (s State) read(ctx context.Context, name kit.RecordName, into any) error {
	held, err := kit.Held(ctx, s.Records, name)
	if err != nil {
		return err
	}
	if len(held.Bytes) == 0 {
		return nil
	}
	return json.Unmarshal(held.Bytes, into)
}

func (s State) write(ctx context.Context, name kit.RecordName, body any) error {
	held, err := kit.Held(ctx, s.Records, name)
	if err != nil {
		return err
	}
	if held.Bytes, err = json.Marshal(body); err != nil {
		return err
	}
	_, err = s.Records.Write(ctx, held)
	return err
}
