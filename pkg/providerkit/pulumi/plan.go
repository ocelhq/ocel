package pulumi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const OpPreview Op = "preview"

func (a *Adapter) Plan(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.ChangeGroup, error) {
	setup, err := a.setup(ctx, plan, OpPreview, report)
	if err != nil {
		return providerkit.ChangeGroup{}, err
	}
	changes, err := a.engine().Preview(ctx, setup, report)
	if err != nil {
		return providerkit.ChangeGroup{}, busy(err, setup)
	}
	group := providerkit.ChangeGroup{
		Kind:    providerkit.StackGroupKind,
		Name:    plan.Ref.Name.String(),
		Changes: changes,
	}
	group.Action, group.Reason = providerkit.RollUp(group.Changes)
	return group, nil
}

func (a *Adapter) PlanDestroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (providerkit.ChangeGroup, error) {
	setup, err := a.setup(ctx, providerkit.StackPlan{Ref: ref}, OpDestroy, report)
	if err != nil {
		return providerkit.ChangeGroup{}, err
	}
	changes, err := a.engine().PreviewDestroy(ctx, setup, report)
	if err != nil {
		return providerkit.ChangeGroup{}, busy(err, setup)
	}
	group := providerkit.ChangeGroup{
		Kind:    providerkit.StackGroupKind,
		Name:    ref.Name.String(),
		Changes: changes,
	}
	group.Action, group.Reason = providerkit.RollUp(group.Changes)
	return group, nil
}

func (autoEngine) Preview(ctx context.Context, setup Setup, report providerkit.Reporter) ([]providerkit.Change, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), setup.Program, setup.Options...)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", setup.Stack, err)
	}
	if err := applyConfig(ctx, stack, setup.Config); err != nil {
		return nil, err
	}

	engineEvents := make(chan events.EngineEvent, 256)
	collected := collectChanges(engineEvents, report)

	opts := []optpreview.Option{
		optpreview.Parallel(setup.Parallel),
		optpreview.EventStreams(engineEvents),
		optpreview.SuppressProgress(),
	}
	if setup.Refresh {
		opts = append(opts, optpreview.Refresh())
	}

	if _, err := stack.Preview(ctx, opts...); err != nil {
		return nil, fmt.Errorf("preview stack %s: %w", setup.Stack, err)
	}
	return awaitChanges(collected), nil
}

func (autoEngine) PreviewDestroy(ctx context.Context, setup Setup, report providerkit.Reporter) ([]providerkit.Change, error) {
	stack, err := auto.SelectStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), nil, setup.Options...)
	if auto.IsSelectStack404Error(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select stack %s: %w", setup.Stack, err)
	}

	outputs, err := stack.Outputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read what stack %s already provisions: %w", setup.Stack, err)
	}
	changes := make([]providerkit.Change, 0, len(outputs))
	for name := range outputs {
		changes = append(changes, providerkit.Change{Kind: "output", Name: name, Action: providerkit.ActionDelete})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes, nil
}

type changeCollector struct {
	changes []providerkit.Change
	done    chan struct{}
}

func collectChanges(stream <-chan events.EngineEvent, report providerkit.Reporter) *changeCollector {
	c := &changeCollector{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		seen := map[string]bool{}
		for ev := range stream {
			if ev.ResourcePreEvent == nil {
				continue
			}
			m := ev.ResourcePreEvent.Metadata
			if seen[m.URN] {
				continue
			}
			seen[m.URN] = true
			action, renders := planAction(m.Op)
			if !renders || !remote(m.Type) {
				continue
			}
			c.changes = append(c.changes, providerkit.Change{
				Kind:   capIdentifier(m.Type),
				Name:   resourceNameFromURN(m.URN),
				Action: action,
			})
			if report != nil && len(c.changes)%25 == 0 {
				report.Say(fmt.Sprintf("Planned %d resources", len(c.changes)))
			}
		}
	}()
	return c
}

func awaitChanges(c *changeCollector) []providerkit.Change {
	select {
	case <-c.done:
	case <-time.After(engineDrainGrace):
	}
	sort.Slice(c.changes, func(i, j int) bool {
		if c.changes[i].Kind != c.changes[j].Kind {
			return c.changes[i].Kind < c.changes[j].Kind
		}
		return c.changes[i].Name < c.changes[j].Name
	})
	return c.changes
}

func remote(typ string) bool {
	return typ != "pulumi:pulumi:Stack" && !strings.HasPrefix(typ, "pulumi:providers:")
}

func planAction(op apitype.OpType) (providerkit.ChangeAction, bool) {
	switch op {
	case apitype.OpSame:
		return providerkit.ActionKeep, true
	case apitype.OpCreate, apitype.OpImport:
		return providerkit.ActionCreate, true
	case apitype.OpUpdate:
		return providerkit.ActionUpdate, true
	case apitype.OpDelete:
		return providerkit.ActionDelete, true
	case apitype.OpReplace, apitype.OpCreateReplacement, apitype.OpDeleteReplaced,
		apitype.OpReadReplacement, apitype.OpImportReplacement:
		return providerkit.ActionReplace, true
	default:
		return "", false
	}
}
