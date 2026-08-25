package vps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type stamp struct {
	Schema uint32 `json:"schema"`
	Digest string `json:"digest"`
	Writer string `json:"writer"`
}

type hostBootstrapper struct{ machine *host }

func (hostBootstrapper) Catalogue() []providerkit.Feature { return nil }

func (b hostBootstrapper) Describe(ctx context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	if err := b.machine.probe(ctx); err != nil {
		return providerkit.Bootstrap{}, err
	}
	got, err := b.machine.check(ctx, b.machine.sudo(shellQuote("cat", stampPath(class)))+" 2>/dev/null || true")
	if err != nil {
		return providerkit.Bootstrap{}, err
	}
	if strings.TrimSpace(got) == "" {
		return providerkit.Bootstrap{Class: class}, nil
	}
	var recorded stamp
	if err := json.Unmarshal([]byte(got), &recorded); err != nil {
		return providerkit.Bootstrap{}, providerkit.Refuse(providerkit.CodeInvalid, "%s is not readable as a stamp: %v", stampPath(class), err)
	}
	return providerkit.Bootstrap{
		Class:   class,
		Present: true,
		Stacks: []providerkit.BootstrapStack{{
			Name:          stackName,
			Present:       true,
			Schema:        recorded.Schema,
			DigestCurrent: recorded.Digest == wouldWriteDigest(coreItems(class)),
			Writer:        recorded.Writer,
		}},
	}, nil
}

func (b hostBootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.BootstrapPlan, error) {
	described, err := b.Describe(ctx, req.Class)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	changes, err := b.survey(ctx, req.Class)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	if len(changes) == 0 {
		if !described.Present {
			return providerkit.BootstrapPlan{}, nil
		}
		return providerkit.BootstrapPlan{Groups: []providerkit.ChangeGroup{{
			Kind: providerkit.StackGroupKind, Name: stackName,
			Action: providerkit.ActionKeep, Reason: "already current",
		}}}, nil
	}
	action, reason := providerkit.RollUp(changes)
	if !described.Present {
		action, reason = providerkit.ActionCreate, "the core stack is not on this machine"
	}
	slow := false
	for _, change := range changes {
		slow = slow || change.Slow
	}
	return providerkit.BootstrapPlan{Groups: []providerkit.ChangeGroup{{
		Kind: providerkit.StackGroupKind, Name: stackName,
		Action: action, Reason: reason, Slow: slow, Changes: changes,
	}}}, nil
}

func (b hostBootstrapper) survey(ctx context.Context, class providerkit.Class) ([]providerkit.Change, error) {
	if err := b.machine.probe(ctx); err != nil {
		return nil, err
	}
	var changes []providerkit.Change
	for _, each := range coreItems(class) {
		present, err := each.present(ctx, *b.machine)
		if err != nil {
			return nil, err
		}
		if present {
			continue
		}
		reason := each.reason
		if reason == "" {
			reason = "not on this machine"
		}
		changes = append(changes, providerkit.Change{
			Kind: each.kind, Name: each.name, Action: providerkit.ActionCreate,
			Reason: reason, Slow: each.kind == "docker:engine",
		})
	}
	return changes, nil
}

func (b hostBootstrapper) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if err := b.machine.probe(ctx); err != nil {
		return err
	}
	items := coreItems(req.Class)
	for _, each := range items {
		present, err := each.present(ctx, *b.machine)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if req.Heal && !healable(each, req.Class) {
			return providerkit.Refuse(providerkit.CodeDenied,
				"healing runs as %s and may only reassert what it owns under %s; %s %s needs a consented bootstrap",
				deployUser, stateDir(req.Class), each.kind, each.name)
		}
		if report != nil {
			report.Say(fmt.Sprintf("creating %s %s", each.kind, each.name))
		}
		if err := each.create(ctx, *b.machine); err != nil {
			return err
		}
	}
	written, err := json.Marshal(stamp{
		Schema: providerkit.BootstrapSchema,
		Digest: wouldWriteDigest(items),
		Writer: string(req.Writer),
	})
	if err != nil {
		return err
	}
	if report != nil {
		report.Say("stamping " + stampPath(req.Class))
	}
	return b.machine.write(ctx, stampPath(req.Class), string(written)+"\n", "root", "0644")
}

func healable(each item, class providerkit.Class) bool {
	return each.kind == "fs:dir" && strings.HasPrefix(each.name, stateDir(class))
}

func (b hostBootstrapper) PlanRemoval(ctx context.Context, class providerkit.Class) (providerkit.BootstrapPlan, error) {
	if err := b.machine.probe(ctx); err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	last, err := b.lastClass(ctx, class)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	var changes []providerkit.Change
	for _, each := range coreItems(class) {
		change := providerkit.Change{Kind: each.kind, Name: each.name, Action: providerkit.ActionDelete}
		if each.data {
			change.Reason = each.reason
		}
		if each.remove == nil {
			change.Action, change.Reason = providerkit.ActionKeep, "installed system-wide; left in place"
		} else if each.singleton && !last {
			change.Action, change.Reason = providerkit.ActionKeep, "another class still uses it"
		}
		changes = append(changes, change)
	}
	changes = append(changes, providerkit.Change{
		Kind: "fs:file", Name: stampPath(class), Action: providerkit.ActionDelete,
	})
	action, reason := providerkit.RollUp(changes)
	return providerkit.BootstrapPlan{Groups: []providerkit.ChangeGroup{{
		Kind: providerkit.StackGroupKind, Name: "vps/" + b.machine.dest(),
		Action: action, Reason: reason, Changes: changes,
	}}}, nil
}

func (b hostBootstrapper) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
	if err := b.machine.probe(ctx); err != nil {
		return err
	}
	last, err := b.lastClass(ctx, class)
	if err != nil {
		return err
	}
	for _, each := range coreItems(class) {
		if each.remove == nil || (each.singleton && !last) || each.name == etcDir(class) {
			continue
		}
		if report != nil {
			report.Say(fmt.Sprintf("deleting %s %s", each.kind, each.name))
		}
		if err := each.remove(ctx, *b.machine); err != nil {
			return err
		}
	}
	if report != nil {
		report.Say("removing " + stampPath(class))
	}
	return b.machine.must(ctx, b.machine.sudo(shellQuote("rm", "-rf", etcDir(class))))
}

func (b hostBootstrapper) lastClass(ctx context.Context, class providerkit.Class) (bool, error) {
	other := providerkit.ClassPreview
	if class == providerkit.ClassPreview {
		other = providerkit.ClassProduction
	}
	return !b.machine.ok(ctx, b.machine.sudo(shellQuote("test", "-f", stampPath(other)))), nil
}

type hostCredentials struct{ machine *host }

func (c hostCredentials) Whoami(ctx context.Context) (providerkit.Identity, error) {
	if err := c.machine.probe(ctx); err != nil {
		return providerkit.Identity{}, err
	}
	who, err := c.machine.check(ctx, "id -un")
	if err != nil {
		return providerkit.Identity{}, err
	}
	name, err := c.machine.check(ctx, "hostname -f 2>/dev/null || hostname")
	if err != nil {
		return providerkit.Identity{}, err
	}
	print, err := c.machine.fingerprint(ctx)
	if err != nil {
		print = "(unscannable)"
	}
	elevation := "root"
	if !c.machine.root {
		elevation = "passwordless sudo"
	}
	return providerkit.Identity{
		Provider:  Vendor,
		Account:   print,
		Principal: who + "@" + name,
		Details: []providerkit.Detail{
			{Label: "Destination", Value: c.machine.dest()},
			{Label: "Elevation", Value: elevation},
			{Label: "Host key", Value: print},
		},
	}, nil
}

func (c hostCredentials) Permissions(tier providerkit.CredentialTier) (edge.CredentialDocument, error) {
	switch tier {
	case providerkit.TierBootstrap:
		var out strings.Builder
		out.WriteString("The account you SSH in as must be root, or able to run sudo without a password.\n\n")
		out.WriteString("It is used to create, once per machine:\n\n")
		for _, each := range coreItems(providerkit.ClassProduction) {
			fmt.Fprintf(&out, "  %-14s %s\n", each.kind, each.name)
		}
		return edge.CredentialDocument{Heading: "Bootstrap login (vps)", Document: out.String()}, nil
	case providerkit.TierDeploy:
		var out strings.Builder
		fmt.Fprintf(&out, "Deploys log in as %s, a system account with:\n\n", deployUser)
		fmt.Fprintf(&out, "  write access to %s\n", deployHome+"/state/<class>")
		fmt.Fprintf(&out, "  membership of the docker group, which is root-equivalent on this machine\n")
		fmt.Fprintf(&out, "  sudo, without a password, for %s alone\n", sealHelper)
		return edge.CredentialDocument{Heading: "Deploy principal (vps)", Document: out.String()}, nil
	}
	return edge.CredentialDocument{}, providerkit.Refuse(providerkit.CodeInvalid, "no such credential tier %q", tier)
}

var (
	_ providerkit.Bootstrapper = hostBootstrapper{}
	_ providerkit.Credentials  = hostCredentials{}
)
