package providerkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	artifactRootDir = ".ocel/output"

	appsDir = "apps"

	configFileName = "ocel.config.ts"
)

func ArtifactRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return artifactRootDir
	}
	return filepath.Join(wd, artifactRootDir)
}

func AppArtifactRoot(root, app string) string { return filepath.Join(root, appsDir, app) }

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
	App    string
	Need   edge.Need
	Edge   edge.Kind
	Detail edge.NeedDetail
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

type NeedRecord struct {
	Needs    []edge.Need
	InEffect []edge.Need
	Waived   []edge.Need
}

type NeedRecords map[string]NeedRecord

type NeedCheck struct {
	Edge          edge.Edge
	Root          string
	AllowDegraded []string
	Degraded      func(edge.Need, string)
}

func (c NeedCheck) Run(ctx context.Context, manifest *contractv1.Manifest) (NeedRecords, error) {
	if c.Edge == nil {
		return nil, nil
	}
	records := NeedRecords{}
	checker, entitles := c.Edge.(edge.EntitlementChecker)
	entitlement := onceChecked(ctx, checker)

	for _, app := range manifest.GetApps() {
		name := app.GetName()
		desc, present, err := ReadServeDescriptor(c.Root, name)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		record, err := c.forApp(name, desc, entitles, entitlement)
		if err != nil {
			return nil, err
		}
		records[name] = record
	}
	return records, nil
}

func (c NeedCheck) forApp(
	name string,
	desc edge.ServeDescriptor,
	entitles bool,
	entitlement func() (edge.CodeEntitlement, error),
) (NeedRecord, error) {
	var record NeedRecord
	for _, need := range declaredNeeds(desc) {
		if !edge.ValidNeed(need) {
			return NeedRecord{}, &UnknownNeedError{App: name, Need: need}
		}
		detail := desc.Needs[need]
		waived := slices.Contains(c.AllowDegraded, string(need))
		serves := edge.Supports(c.Edge, need)

		if serves && entitles && slices.Contains(edge.CodeNeeds(), need) {
			granted, err := entitlement()
			switch {
			case err != nil:
				return NeedRecord{}, &EdgeEntitlementError{App: name, Need: need, Edge: c.Edge.Kind(), Err: err}
			case granted.Granted == edge.EntitlementGranted,
				granted.Granted == edge.EntitlementUnknown:
			case waived:
				serves = false
			default:
				return NeedRecord{}, &EdgeEntitlementError{App: name, Need: need, Edge: c.Edge.Kind(), Plan: granted.Plan}
			}
		}

		record.Needs = append(record.Needs, need)
		switch {
		case serves:
			record.InEffect = append(record.InEffect, need)
		case waived:
			record.Waived = append(record.Waived, need)
			if c.Degraded != nil {
				c.Degraded(need, fmt.Sprintf("%s: %s. It affects %s", name, degradeOf[need], affected(detail)))
			}
		default:
			return NeedRecord{}, &UnsupportedNeedError{App: name, Need: need, Edge: c.Edge.Kind(), Detail: detail}
		}
	}
	return record, nil
}

func declaredNeeds(desc edge.ServeDescriptor) []edge.Need {
	ordered := make([]edge.Need, 0, len(desc.Needs))
	for _, need := range edge.AllNeeds() {
		if _, declared := desc.Needs[need]; declared {
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

func onceChecked(ctx context.Context, checker edge.EntitlementChecker) func() (edge.CodeEntitlement, error) {
	var entitlement edge.CodeEntitlement
	var err error
	asked := false
	return func() (edge.CodeEntitlement, error) {
		if !asked {
			asked = true
			entitlement, err = checker.CodeEntitlement(ctx)
		}
		return entitlement, err
	}
}

func ReadServeDescriptor(root, app string) (edge.ServeDescriptor, bool, error) {
	raw, err := os.ReadFile(filepath.Join(AppArtifactRoot(root, app), edge.ServeDescriptorFile))
	if errors.Is(err, fs.ErrNotExist) {
		return edge.ServeDescriptor{}, false, nil
	}
	if err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("read serve descriptor for %s: %w", app, err)
	}
	var desc edge.ServeDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("parse serve descriptor for %s: %w", app, err)
	}
	return desc, true, nil
}
