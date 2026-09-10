package alb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type previewEntry struct {
	BaseDomain  string `json:"baseDomain"`
	Certificate string `json:"certificate,omitempty"`
}

func (e *Edge) previewRecord() providerkit.RecordName {
	return append(providerkit.EdgeStacksRecord(edge.ClassPreview), string(Kind), "preview-wildcard")
}

func (e *Edge) heldPreview(ctx context.Context) (previewEntry, error) {
	record, err := providerkit.Held(ctx, e.deps.Records, e.previewRecord())
	if err != nil {
		return previewEntry{}, fmt.Errorf("read which wildcard the %s edge serves previews on: %w", Kind, err)
	}
	var held previewEntry
	if len(record.Bytes) == 0 {
		return held, nil
	}
	if err := json.Unmarshal(record.Bytes, &held); err != nil {
		return previewEntry{}, fmt.Errorf("decode which wildcard the %s edge serves previews on: %w", Kind, err)
	}
	return held, nil
}

func (e *Edge) rememberPreview(ctx context.Context, entry previewEntry) error {
	record, err := providerkit.Held(ctx, e.deps.Records, e.previewRecord())
	if err != nil {
		return fmt.Errorf("read which wildcard the %s edge serves previews on: %w", Kind, err)
	}
	if record.Bytes, err = json.Marshal(entry); err != nil {
		return fmt.Errorf("encode which wildcard the %s edge serves previews on: %w", Kind, err)
	}
	if _, err := e.deps.Records.Write(ctx, record); err != nil {
		return fmt.Errorf("record which wildcard the %s edge serves previews on: %w", Kind, err)
	}
	return nil
}

func (e *Edge) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"the %q edge answers every preview from one wildcard host rule, and this reconcile names no base domain", Kind)
	}
	if spec.Certificate == "" {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"the %q edge terminates TLS for %s at the load balancer, so its certificate map needs the wildcard certificate, and this reconcile carries none",
			Kind, wildcard)
	}
	entry := previewEntry{BaseDomain: spec.BaseDomain, Certificate: spec.Certificate}
	front, err := e.raiseServing(ctx, edge.ClassPreview, entry, edge.DiscardReporter())
	if err != nil {
		return "", err
	}
	if err := e.deps.Routes.Route(ctx, front.URLMap, wildcard, previewBackendName(spec.BaseDomain)); err != nil {
		return "", err
	}
	if err := e.rememberPreview(ctx, entry); err != nil {
		return "", err
	}
	return front.Address, nil
}

func (e *Edge) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	served, err := providerkit.ProjectsServedOnPreview(ctx, e.deps.Records, baseDomain)
	if err != nil {
		return err
	}
	if len(served) > 0 {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s still serves the previews of %s on the %s front of class %s, and releasing the host rule would leave every one of them "+
				"answering a 404 from the load balancer: take those previews down with `ocel destroy preview` in each project first",
			wildcard, strings.Join(served, ", "), Kind, edge.ClassPreview)
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, Target{Class: edge.ClassPreview})
	if err != nil {
		return err
	}
	front := frontOf(outputs)
	if front.URLMap != "" {
		if err := e.deps.Routes.Unroute(ctx, front.URLMap, wildcard); err != nil {
			return err
		}
	}
	if front.standing() {
		if _, err := e.raiseServing(ctx, edge.ClassPreview, previewEntry{}, edge.DiscardReporter()); err != nil {
			return err
		}
	}
	if err := providerkit.Forget(ctx, e.deps.Records, e.previewRecord()); err != nil {
		return fmt.Errorf("release which wildcard the %s edge served previews on: %w", Kind, err)
	}
	return nil
}

func previewResourceName(baseDomain string, role ...string) string {
	segments := []naming.Segment{
		naming.Fixed("ocel"),
		naming.Fixed(string(Kind)),
		naming.Fixed(string(edge.ClassPreview)),
		naming.Compressible(naming.SanitizeHost(baseDomain)),
	}
	for _, each := range role {
		segments = append(segments, naming.Fixed(each))
	}
	return naming.Fit(maxResourceName, naming.WordSeparator, segments...)
}

func previewBackendName(baseDomain string) string { return previewResourceName(baseDomain) }

func previewEntryName(baseDomain string) string { return previewResourceName(baseDomain, "cert") }

func previewNEGName(baseDomain string) string { return previewResourceName(baseDomain, "neg") }
