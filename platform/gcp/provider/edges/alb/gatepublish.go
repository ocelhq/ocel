package alb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

const (
	maxGateBusyRetries         = 12
	maxGateRecordReads         = 5
	maxOlderGateVersions       = 2
	maxGateProbes              = 90
	enforcedProbes             = 5
	probeReminderEveryAttempts = 15
)

func (e *Edge) publishGate(ctx context.Context, publication edge.GatePublication) error {
	if publication.Say != nil {
		publication.Say("Rolling the " + string(publication.State.Tier) + " gate of " + publication.State.Slug + " out to the load balancer's gate plugin, which takes a few minutes")
	}
	opened, err := e.Open(publication.State)
	if err != nil {
		return err
	}
	return opened.(*stack).syncGate(ctx, edge.StackSpec{
		Slug: publication.State.Slug,
		Tier: publication.State.Tier,
		Gate: publication.Gate,
		Warn: publication.Say,
	})
}

type gateProof struct {
	Hosts   []string
	Address string
}

type writtenPublication struct {
	entry       records.Entry
	publication gatePublication
}

func (e *Edge) gateTarget(tier environment.Tier) Target {
	return Target{Tier: tier, Gate: true}
}

func (e *Edge) publishGates(ctx context.Context, tier environment.Tier, proof gateProof, log progress.Log) error {
	image, err := e.deps.Images.Ensure(ctx, tier, e.deps.GatePlugin)
	if err != nil {
		return err
	}
	config, err := e.encodeGateConfig(ctx, tier)
	if err != nil {
		return err
	}
	if err := e.refuseOversizedConfig(tier, config); err != nil {
		return err
	}
	recorded, found, err := e.readGatePublication(ctx, tier)
	if err != nil {
		return err
	}
	if !found || !recorded.Confirmed || recorded.Main != newGateVersion(image, config) {
		if err := e.raiseGate(ctx, tier, image, log); err != nil {
			return err
		}
	}
	return e.proveGated(ctx, proof, log)
}

func (e *Edge) runsRecordedGate(ctx context.Context, tier environment.Tier, slug string, record gateRecord, gated bool) (bool, error) {
	publication, published, err := e.readGatePublication(ctx, tier)
	if err != nil || !published {
		return !gated, err
	}
	if !publication.Confirmed {
		return false, nil
	}
	config, err := gateplugin.ParseConfig(publication.Main.Config)
	if err != nil {
		return false, nil
	}
	held, hosts, running := config.FindProject(slug)
	if !gated || !running {
		return !gated && !running, nil
	}
	return sameGateRecord(gateRecord{Hosts: hosts, Gate: held}, record), nil
}

func (e *Edge) refuseOversizedConfig(tier environment.Tier, config string) error {
	if total := len(e.deps.GatePlugin) + len(config); total > gateplugin.PackageLimitBytes {
		return refusal.Refuse(refusal.CodeInvalid,
			"the keys that gate the %s tier take %d bytes, and Google allows a load balancer plugin and its config %d together, of which the plugin takes %d. "+
				"Revoke bypass secrets with `%s`, or turn protection.%s off for projects that no longer need a gate",
			tier, len(config), gateplugin.PackageLimitBytes, len(e.deps.GatePlugin), revokeBypassCommand(tier), tier)
	}
	return nil
}

func revokeBypassCommand(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel protection bypass revoke --preview"
	}
	return "ocel protection bypass revoke"
}

func (e *Edge) raiseGate(ctx context.Context, tier environment.Tier, image string, log progress.Log) error {
	var written writtenPublication
	program := func(ctx *pulumi.Context, project string) error {
		publication, entry, err := e.recordGatePublication(ctx.Context(), tier, image, !ctx.DryRun())
		if err != nil {
			return err
		}
		written = writtenPublication{entry: entry, publication: publication}
		return gateProgram(gateSpec{
			Names:  loadBalancerNames(tier, false),
			Main:   publication.Main,
			Before: publication.Before,
		})(ctx, project)
	}
	for attempt := 1; ; attempt++ {
		_, err := e.deps.Stacks.Up(ctx, e.gateTarget(tier), program, log)
		if err == nil {
			break
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy || attempt == maxGateBusyRetries || !e.deps.WaitBeforeRetry(ctx, attempt) {
			return err
		}
	}
	return e.confirmGatePublication(ctx, tier, written)
}

func (e *Edge) recordGatePublication(ctx context.Context, tier environment.Tier, image string, write bool) (gatePublication, records.Entry, error) {
	for range maxGateRecordReads {
		config, err := e.encodeGateConfig(ctx, tier)
		if err != nil {
			return gatePublication{}, records.Entry{}, err
		}
		if err := e.refuseOversizedConfig(tier, config); err != nil {
			return gatePublication{}, records.Entry{}, err
		}
		entry, recorded, found, err := e.readGatePublicationEntry(ctx, tier)
		if err != nil {
			return gatePublication{}, records.Entry{}, err
		}
		publication := gatePublication{Main: newGateVersion(image, config)}
		if found {
			publication.Before = listRunnableVersions(recorded, publication.Main)
		}
		if !write {
			return publication, entry, nil
		}
		entry, err = e.writeGatePublication(ctx, tier, entry, publication)
		if !errors.Is(err, records.ErrStale) {
			return publication, entry, err
		}
	}
	return gatePublication{}, records.Entry{}, refusal.Refuse(refusal.CodeBusy,
		"what the gate plugin of the %s edge's %s load balancer runs was rewritten between every read of it and the write that followed: run this again once other deploys have finished", Kind, tier)
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

func (e *Edge) confirmGatePublication(ctx context.Context, tier environment.Tier, written writtenPublication) error {
	if len(written.entry.Value) == 0 {
		return nil
	}
	written.publication.Confirmed = true
	_, err := e.writeGatePublication(ctx, tier, written.entry, written.publication)
	if errors.Is(err, records.ErrStale) {
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
	log.Say("Waiting for the load balancer to enforce the gate on " + strings.Join(slices.Sorted(maps.Keys(pending)), ", ") +
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
			return refuseUnenforced(slices.Sorted(maps.Keys(pending)), last)
		}
		if attempt%probeReminderEveryAttempts == 0 {
			log.Say("Still waiting for the gate on " + strings.Join(slices.Sorted(maps.Keys(pending)), ", "))
		}
		if !e.deps.WaitBeforeRetry(ctx, attempt) {
			return ctx.Err()
		}
	}
}

func refuseUnenforced(hosts []string, last error) error {
	reason := "answered without the gate's refusal"
	if last != nil {
		reason = "could not be reached: " + last.Error()
	}
	return fmt.Errorf("the gate was published but %s %s after %d requests over several minutes, "+
		"and a host the gate does not reach would serve its protected project to anyone, so it is left unrouted: run the deploy again once the load balancer has taken the gate",
		strings.Join(hosts, ", "), reason, maxGateProbes)
}
