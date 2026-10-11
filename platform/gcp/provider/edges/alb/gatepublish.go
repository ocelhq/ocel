package alb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

const (
	maxGateBusyRetries   = 12
	maxGateRecordReads   = 5
	maxOlderGateVersions = 2
	maxGateProbes        = 90
	enforcedProbes       = 5
	probeReminderEvery   = 15
)

type gateProof struct {
	Hosts   []string
	Address string
}

type writtenPublication struct {
	entry       keyvalue.Entry
	publication gatePublication
}

func (e *Edge) gateTarget() Target {
	return Target{Tier: environment.TierPreview, Gate: true}
}

func (e *Edge) publishGates(ctx context.Context, proof gateProof, log progress.Log) error {
	image, err := e.deps.Images.EnsureImage(ctx, e.deps.GatePlugin)
	if err != nil {
		return err
	}
	config, err := e.encodeGateConfig(ctx)
	if err != nil {
		return err
	}
	if err := e.refuseOversizedConfig(config); err != nil {
		return err
	}
	recorded, found, err := e.readGatePublication(ctx)
	if err != nil {
		return err
	}
	if !found || !recorded.Confirmed || recorded.Main != newGateVersion(image, config) {
		if err := e.raiseGate(ctx, image, log); err != nil {
			return err
		}
	}
	return e.proveGated(ctx, proof, log)
}

func (e *Edge) refuseOversizedConfig(config string) error {
	if total := len(e.deps.GatePlugin) + len(config); total > gateplugin.PackageLimitBytes {
		return refusal.Refuse(refusal.CodeInvalid,
			"the keys that gate previews take %d bytes, and Google allows a load balancer plugin and its config %d together, of which the plugin takes %d. "+
				"Revoke bypass secrets or shorten the allowed OPTIONS prefixes, or destroy the previews of projects that no longer need a gate",
			len(config), gateplugin.PackageLimitBytes, len(e.deps.GatePlugin))
	}
	return nil
}

func (e *Edge) raiseGate(ctx context.Context, image string, log progress.Log) error {
	var written writtenPublication
	program := func(ctx *pulumi.Context, project string) error {
		publication, entry, err := e.recordGatePublication(ctx.Context(), image, !ctx.DryRun())
		if err != nil {
			return err
		}
		written = writtenPublication{entry: entry, publication: publication}
		return gateProgram(gateSpec{
			Names:  loadBalancerNames(environment.TierPreview, false),
			Main:   publication.Main,
			Before: publication.Before,
		})(ctx, project)
	}
	for attempt := 1; ; attempt++ {
		_, err := e.deps.Stacks.Up(ctx, e.gateTarget(), program, log)
		if err == nil {
			break
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy || attempt == maxGateBusyRetries || !e.deps.WaitBeforeRetry(ctx, attempt) {
			return err
		}
	}
	return e.confirmGatePublication(ctx, written)
}

func (e *Edge) recordGatePublication(ctx context.Context, image string, write bool) (gatePublication, keyvalue.Entry, error) {
	for range maxGateRecordReads {
		config, err := e.encodeGateConfig(ctx)
		if err != nil {
			return gatePublication{}, keyvalue.Entry{}, err
		}
		if err := e.refuseOversizedConfig(config); err != nil {
			return gatePublication{}, keyvalue.Entry{}, err
		}
		entry, recorded, found, err := e.readGatePublicationEntry(ctx)
		if err != nil {
			return gatePublication{}, keyvalue.Entry{}, err
		}
		publication := gatePublication{Main: newGateVersion(image, config)}
		if found {
			publication.Before = listRunnableVersions(recorded, publication.Main)
		}
		if !write {
			return publication, entry, nil
		}
		entry, err = e.writeGatePublication(ctx, entry, publication)
		if !errors.Is(err, keyvalue.ErrStale) {
			return publication, entry, err
		}
	}
	return gatePublication{}, keyvalue.Entry{}, refusal.Refuse(refusal.CodeBusy,
		"what the %s edge's gate plugin runs was rewritten between every read of it and the write that followed: run this again once other deploys have finished", Kind)
}

func listRunnableVersions(recorded gatePublication, next gateVersion) []gateVersion {
	candidates := []gateVersion{recorded.Main}
	if !recorded.Confirmed {
		candidates = append(candidates, recorded.Before...)
	}
	var runnable []gateVersion
	for _, version := range candidates {
		if len(runnable) == maxOlderGateVersions {
			break
		}
		if version.ID != next.ID && !slices.ContainsFunc(runnable, func(kept gateVersion) bool { return kept.ID == version.ID }) {
			runnable = append(runnable, version)
		}
	}
	return runnable
}

func (e *Edge) confirmGatePublication(ctx context.Context, written writtenPublication) error {
	if len(written.entry.Value) == 0 {
		return nil
	}
	written.publication.Confirmed = true
	_, err := e.writeGatePublication(ctx, written.entry, written.publication)
	if errors.Is(err, keyvalue.ErrStale) {
		return nil
	}
	return err
}

func (e *Edge) proveGated(ctx context.Context, proof gateProof, log progress.Log) error {
	if len(proof.Hosts) == 0 {
		return nil
	}
	pending := map[string]int{}
	for _, host := range proof.Hosts {
		pending[host] = 0
	}
	log.Say("Waiting for the load balancer to enforce the preview gate on " + strings.Join(slices.Sorted(maps.Keys(pending)), ", ") +
		": Google takes a few minutes to carry a new gate to every location that serves requests")
	var last error
	for attempt := 1; ; attempt++ {
		for _, host := range slices.Sorted(maps.Keys(pending)) {
			enforced, err := e.deps.ProbeGate(ctx, host, proof.Address)
			switch {
			case err != nil:
				last, pending[host] = err, 0
			case !enforced:
				pending[host] = 0
			case pending[host]+1 >= enforcedProbes:
				delete(pending, host)
			default:
				pending[host]++
			}
		}
		if len(pending) == 0 {
			return nil
		}
		if attempt == maxGateProbes {
			return unenforced(slices.Sorted(maps.Keys(pending)), last)
		}
		if attempt%probeReminderEvery == 0 {
			log.Say("Still waiting for the preview gate on " + strings.Join(slices.Sorted(maps.Keys(pending)), ", "))
		}
		if !e.deps.WaitBeforeRetry(ctx, attempt) {
			return ctx.Err()
		}
	}
}

func unenforced(hosts []string, last error) error {
	reason := "answered without the gate's refusal"
	if last != nil {
		reason = "could not be reached: " + last.Error()
	}
	return fmt.Errorf("the preview gate was published but %s %s after %d requests over several minutes, "+
		"and a host the gate does not reach would serve its preview to anyone, so it is left unrouted: run the deploy again once the load balancer has taken the gate",
		strings.Join(hosts, ", "), reason, maxGateProbes)
}
