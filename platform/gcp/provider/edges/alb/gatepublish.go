package alb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

const (
	maxGatePublications = 4
	maxGateBusyRetries  = 12
	maxGateProbes       = 90
	enforcedProbes      = 5
	probeReminderEvery  = 15
)

type gateProof struct {
	Hosts   []string
	Address string
}

func (e *Edge) gateTarget() Target {
	return Target{Tier: environment.TierPreview, Gate: true}
}

func (e *Edge) publishGates(ctx context.Context, proof gateProof, log progress.Log) error {
	image, err := e.deps.Images.EnsureImage(ctx, e.deps.GatePlugin)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		entries, err := e.listGateEntries(ctx)
		if err != nil {
			return err
		}
		snapshot, err := e.openGateRecords(ctx, entries)
		if err != nil {
			return err
		}
		config := snapshot.encodeConfig(gateplugin.EncodeConfig)
		if err := e.refuseOversizedConfig(config); err != nil {
			return err
		}
		if err := e.raiseGate(ctx, image, config, log); err != nil {
			return err
		}
		after, err := e.listGateEntries(ctx)
		if err != nil {
			return err
		}
		if maps.Equal(snapshot.revisions, revisionsOf(after)) {
			break
		}
		if attempt == maxGatePublications {
			return refusal.Refuse(refusal.CodeBusy,
				"the preview gates changed under %d publications in a row: other deploys are changing them, so run this again once they have finished", attempt)
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

func (e *Edge) raiseGate(ctx context.Context, image, config string, log progress.Log) error {
	held, found, err := e.readGatePublication(ctx)
	if err != nil {
		return err
	}
	next := newGateVersion(image, config)
	if found && held.Main == next {
		return nil
	}
	publication := gatePublication{Main: next}
	if found {
		before := held.Main
		publication.Before = &before
	}
	program := gateProgram(gateSpec{
		Names:  loadBalancerNames(environment.TierPreview, false),
		Main:   publication.Main,
		Before: publication.Before,
	})
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
	return e.writeGatePublication(ctx, publication)
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
