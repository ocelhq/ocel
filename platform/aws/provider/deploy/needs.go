package deploy

import (
	"context"
	"fmt"
	"slices"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const configFileName = "ocel.config.ts"

var degradeOf = map[edge.Need]string{
	edge.NeedEdgeMiddleware: "middleware runs in the origin's Node server the way `next start` runs it, so every request pays the round trip to the origin before it is routed",
	edge.NeedEdgeRuntime:    "routes declaring the edge runtime run in the origin's Node server the way `next start` runs them, so they answer from one region instead of near the reader",
	edge.NeedPPRResume:      "the prerendered shell and the resumed stream both come from the origin the way `next start` serves them, so the shell no longer arrives before the origin is reached",
	edge.NeedEdgeCache:      "responses are cached at the origin only the way `next start` caches them, so every hit crosses the network to the origin",
	edge.NeedStreaming:      "responses are buffered before they leave the origin the way `next start` answers without an edge in front, so the first byte waits on the last",
}

type UnknownNeedError struct {
	App  string
	Need edge.Need
}

func (e *UnknownNeedError) Error() string {
	return fmt.Sprintf(
		"app %s declares the need %q, which no edge knows: the needs an app may declare are %s. "+
			"Rebuild the app with a CLI that speaks the same need set, or drop it from its %s",
		e.App, e.Need, strings.Join(edge.NeedNames(edge.AllNeeds()), ", "), edge.ServeDescriptorFile,
	)
}

type UnsupportedNeedError struct {
	App      string
	Need     edge.Need
	Edge     edge.Kind
	Detail   edge.NeedDetail
	AllowKey string
}

func (e *UnsupportedNeedError) Error() string {
	return fmt.Sprintf(
		"app %s needs %s and the %s edge does not serve it: %s. It affects %s. "+
			"Add %q to `allowDegraded` in %s to deploy it degraded, or move the app to an edge that serves %s",
		e.App, e.Need, e.Edge, degradeOf[e.Need], affected(e.Detail),
		string(e.Need), configFileName, e.Need,
	)
}

type EdgeEntitlementError struct {
	App  string
	Need edge.Need
	Edge edge.Kind
	Plan string
	Err  error
}

func (e *EdgeEntitlementError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf(
			"app %s needs %s, which runs your code at the %s edge, and this deploy could not confirm the account may run it: %v",
			e.App, e.Need, e.Edge, e.Err,
		)
	}
	plan := "the plan it is on"
	if e.Plan != "" {
		plan = "the " + e.Plan + " plan"
	}
	return fmt.Sprintf(
		"app %s needs %s, which runs your code at the %s edge, and %s does not run code at the edge. "+
			"Upgrade the account, or add %q to `allowDegraded` in %s to run it on the origin instead",
		e.App, e.Need, e.Edge, plan, string(e.Need), configFileName,
	)
}

func (e *EdgeEntitlementError) Unwrap() error { return e.Err }

func affected(detail edge.NeedDetail) string {
	if len(detail.Routes) > 0 {
		return "routes " + strings.Join(detail.Routes, ", ")
	}
	if len(detail.Matchers) > 0 {
		return "the routes matching " + strings.Join(detail.Matchers, ", ")
	}
	if detail.Count == 1 {
		return "1 route"
	}
	return fmt.Sprintf("%d routes", detail.Count)
}

func declaredNeeds(desc edge.ServeDescriptor) []edge.Need {
	ordered := make([]edge.Need, 0, len(desc.Needs))
	for _, need := range edge.AllNeeds() {
		if _, ok := desc.Needs[need]; ok {
			ordered = append(ordered, need)
		}
	}
	unknown := make([]string, 0)
	for need := range desc.Needs {
		if !edge.ValidNeed(need) {
			unknown = append(unknown, string(need))
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		ordered = append(ordered, edge.Need(name))
	}
	return ordered
}

type degradedNeed struct {
	need   edge.Need
	detail string
}

type needRecord struct {
	needs    []edge.Need
	inEffect []edge.Need
	waived   []edge.Need
}

type needRecords map[string]needRecord

func (r needRecords) forApp(app string) (needs, inEffect, waived []edge.Need) {
	rec := r[app]
	return rec.needs, rec.inEffect, rec.waived
}

func checkNeeds(ctx context.Context, cfg Config, manifest *contractv1.Manifest) (needRecords, error) {
	if cfg.Edge == nil {
		return nil, nil
	}

	records := needRecords{}
	var degraded []degradedNeed
	verifier, verifies := cfg.Edge.(edge.CredentialVerifier)
	entitlement := onceVerified(ctx, verifier)

	for _, app := range manifestApps(manifest) {
		name := app.GetName()
		desc, ok, err := readServeDescriptor(cfg.ArtifactRoot, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var rec needRecord
		for _, need := range declaredNeeds(desc) {
			if !edge.ValidNeed(need) {
				return nil, &UnknownNeedError{App: name, Need: need}
			}
			detail := desc.Needs[need]
			waived := slices.Contains(cfg.AllowDegraded, string(need))
			serves := edge.Supports(cfg.Edge, need)

			if serves && verifies && slices.Contains(edge.CodeNeeds(), need) {
				identity, err := entitlement()
				switch {
				case err != nil:
					return nil, &EdgeEntitlementError{App: name, Need: need, Edge: cfg.Edge.Kind(), Err: err}
				case identity.CodeEntitlement == edge.EntitlementGranted,
					identity.CodeEntitlement == edge.EntitlementUnknown:
				case waived:
					serves = false
				default:
					return nil, &EdgeEntitlementError{App: name, Need: need, Edge: cfg.Edge.Kind(), Plan: identity.Plan}
				}
			}

			rec.needs = append(rec.needs, need)
			switch {
			case serves:
				rec.inEffect = append(rec.inEffect, need)
			case waived:
				rec.waived = append(rec.waived, need)
				degraded = append(degraded, degradedNeed{
					need:   need,
					detail: fmt.Sprintf("%s: %s. It affects %s", name, degradeOf[need], affected(detail)),
				})
			default:
				return nil, &UnsupportedNeedError{App: name, Need: need, Edge: cfg.Edge.Kind(), Detail: detail}
			}
		}
		records[name] = rec
	}

	if cfg.Degraded != nil {
		for _, d := range degraded {
			cfg.Degraded(d.need, d.detail)
		}
	}
	return records, nil
}

func onceVerified(ctx context.Context, verifier edge.CredentialVerifier) func() (edge.CredentialIdentity, error) {
	var identity edge.CredentialIdentity
	var err error
	asked := false
	return func() (edge.CredentialIdentity, error) {
		if !asked {
			asked = true
			identity, err = verifier.VerifyCredentials(ctx)
		}
		return identity, err
	}
}
