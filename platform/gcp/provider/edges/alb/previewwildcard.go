package alb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func (e *Edge) previewKey() records.Key {
	return stackrecords.EdgeStacksPartition(environment.TierPreview).Key(string(albresources.Kind), "preview-wildcard")
}

func (e *Edge) recordedPreview(ctx context.Context) (albresources.PreviewEntry, error) {
	entry, err := records.ReadOrEmpty(ctx, e.deps.KeyValues, e.previewKey())
	if err != nil {
		return albresources.PreviewEntry{}, fmt.Errorf("read which wildcard the %s edge serves previews on: %w", albresources.Kind, err)
	}
	var preview albresources.PreviewEntry
	if len(entry.Value) == 0 {
		return preview, nil
	}
	if err := json.Unmarshal(entry.Value, &preview); err != nil {
		return albresources.PreviewEntry{}, fmt.Errorf("decode which wildcard the %s edge serves previews on: %w", albresources.Kind, err)
	}
	return preview, nil
}

func (e *Edge) rememberPreview(ctx context.Context, preview albresources.PreviewEntry) error {
	entry, err := records.ReadOrEmpty(ctx, e.deps.KeyValues, e.previewKey())
	if err != nil {
		return fmt.Errorf("read which wildcard the %s edge serves previews on: %w", albresources.Kind, err)
	}
	if entry.Value, err = json.Marshal(preview); err != nil {
		return fmt.Errorf("encode which wildcard the %s edge serves previews on: %w", albresources.Kind, err)
	}
	if _, err := e.deps.KeyValues.Write(ctx, entry); err != nil {
		return fmt.Errorf("record which wildcard the %s edge serves previews on: %w", albresources.Kind, err)
	}
	return nil
}

func (e *Edge) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the %q edge answers previews under one wildcard certificate, and this reconcile names no base domain", albresources.Kind)
	}
	if spec.Certificate == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the %q edge terminates TLS for %s at the load balancer, so its certificate map needs the wildcard certificate, and this reconcile names none",
			albresources.Kind, wildcard)
	}
	if err := e.deps.Engine.FrontPart.Claim(ctx, environment.TierPreview, wildcard); err != nil {
		return "", err
	}
	entry := albresources.PreviewEntry{BaseDomain: spec.BaseDomain, Certificate: spec.Certificate}
	balancer, err := e.raiseServing(ctx, environment.TierPreview, entry, "", progress.Discard())
	if err != nil {
		return "", err
	}
	if err := e.rememberPreview(ctx, entry); err != nil {
		return "", err
	}
	return balancer.Address, nil
}

func (e *Edge) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	served, err := stackrecords.ProjectsServedOnPreview(ctx, e.deps.KeyValues, baseDomain)
	if err != nil {
		return err
	}
	if len(served) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s still serves the previews of %s on the %s load balancer of tier %s, and releasing its certificate would leave every one of them "+
				"refusing TLS: take those previews down with `ocel projects destroy --tier preview` in each project first",
			wildcard, strings.Join(served, ", "), albresources.Kind, environment.TierPreview)
	}
	outputs, err := e.deps.Engine.Stacks.Outputs(ctx, e.loadBalancerStack(environment.TierPreview))
	if err != nil {
		return err
	}
	if loadBalancerOf(outputs).provisioned() {
		if _, err := e.raiseServing(ctx, environment.TierPreview, albresources.PreviewEntry{}, "", progress.Discard()); err != nil {
			return err
		}
	}
	if err := records.Forget(ctx, e.deps.KeyValues, e.previewKey()); err != nil {
		return fmt.Errorf("release which wildcard the %s edge served previews on: %w", albresources.Kind, err)
	}
	return e.deps.Engine.FrontPart.Release(ctx, environment.TierPreview, wildcard)
}
