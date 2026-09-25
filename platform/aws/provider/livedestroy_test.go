package provider_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func TestLiveDestroyNamesWhatIsStrandedAndLeavesNothingStanding(t *testing.T) {
	a := live(t)
	class := providerkit.ClassProduction
	boot := a.emptied(t, class)
	ctx := context.Background()

	if err := boot.Apply(ctx, providerkit.BootstrapRequest{Class: class, WrittenBy: liveWriter}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	held, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(a.aws), defaultNamespace, string(class))
	if err != nil {
		t.Fatal(err)
	}

	removal, err := boot.PlanRemove(ctx, class)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	leaving := groupNamed(t, removal, "aws/"+coreStackName)
	if leaving.Action != providerkit.ActionDelete {
		t.Errorf("PlanRemove() plans %s as %q, want %q", leaving.Name, leaving.Action, providerkit.ActionDelete)
	}
	for _, unrecoverable := range []string{"StateBucket", "ArtifactBucket", "AssetBucket", "VarsTable"} {
		if reason := changeFor(leaving, unrecoverable).Reason; reason == "" {
			t.Errorf("PlanRemove() takes %s with no reason, and the typed confirmation must name what is unrecoverable before a user types", unrecoverable)
		}
	}
	if !changeFor(leaving, "StateBucket").Slow {
		t.Error("PlanRemove() takes the state bucket without warning it is slow, and a destroy that seems hung is one a user interrupts half way")
	}
	dropping := groupNamed(t, removal, "aws/"+bootstrap.ParamGroupName)
	passphrase := changeFor(dropping, passphraseParam)
	if passphrase.Action != providerkit.ActionDelete {
		t.Errorf("PlanRemove() plans the passphrase as %q, want the last class on this account to take it", passphrase.Action)
	}
	if passphrase.Reason == "" {
		t.Error("PlanRemove() takes the passphrase with no reason, and every Pulumi state in this account is encrypted under it")
	}

	if err := boot.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	gone, err := boot.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after Remove() = %v", err)
	}
	if gone.Present {
		t.Error("Describe() still claims a bootstrap after Remove()")
	}
	if status := a.stackStatus(t, coreStackName); status != "" && status != "DELETE_COMPLETE" {
		t.Errorf("%s stands at %q after Remove(), want it gone", coreStackName, status)
	}
	for _, bucket := range []string{held.StateBucket, held.ArtifactBucket, held.AssetBucket} {
		if a.bucketStands(t, bucket) {
			t.Errorf("%s still answers after Remove(), and a destroy leaves no bytes behind", bucket)
		}
	}
	for _, table := range []string{held.StateTable, held.VarsTable} {
		if a.tableStands(t, table) {
			t.Errorf("%s still answers after Remove()", table)
		}
	}
	origin, err := defaultNamespace.OriginSecretParamFor(string(class))
	if err != nil {
		t.Fatal(err)
	}
	for _, param := range []string{origin, passphraseParam} {
		if a.paramStands(t, param) {
			t.Errorf("%s still stands after Remove()", param)
		}
	}

	if err := boot.Remove(ctx, class, nil); err != nil {
		t.Errorf("a second Remove() = %v, want an already-forgotten account to be a no-op", err)
	}

	again, err := boot.Plan(ctx, providerkit.BootstrapRequest{Class: class, WrittenBy: liveWriter})
	if err != nil {
		t.Fatalf("Plan() after Remove() = %v", err)
	}
	if group := groupNamed(t, again, "aws/"+coreStackName); group.Action != providerkit.ActionCreate {
		t.Errorf("Plan() over a destroyed account plans %q, want %q", group.Action, providerkit.ActionCreate)
	}
}

func TestLiveDestroyingOneClassLeavesTheSiblingAndThePassphraseItSharesStanding(t *testing.T) {
	a := live(t)
	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	boot := a.emptied(t, production, preview)
	ctx := context.Background()

	for _, class := range []providerkit.Class{production, preview} {
		if err := boot.Apply(ctx, providerkit.BootstrapRequest{Class: class, WrittenBy: liveWriter}, nil); err != nil {
			t.Fatalf("Apply(%s) = %v", class, err)
		}
	}

	beside, err := boot.PlanRemove(ctx, production)
	if err != nil {
		t.Fatalf("PlanRemove(%s) = %v", production, err)
	}
	shared := changeFor(groupNamed(t, beside, "aws/"+bootstrap.ParamGroupName), passphraseParam)
	if shared.Action != providerkit.ActionKeep {
		t.Errorf("destroying %s plans the passphrase as %q while %s still stands on this account", production, shared.Action, preview)
	}
	if !strings.Contains(shared.Reason, string(preview)) {
		t.Errorf("the passphrase is kept with the reason %q, want the sibling that still needs it named", shared.Reason)
	}

	if err := boot.Remove(ctx, production, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", production, err)
	}
	if !a.paramStands(t, passphraseParam) {
		t.Errorf("the passphrase went with %s, and every Pulumi state %s holds is encrypted under it", production, preview)
	}
	if status := a.stackStatus(t, previewStackName); status != "CREATE_COMPLETE" {
		t.Errorf("%s stands at %q after its sibling was destroyed, want CREATE_COMPLETE", previewStackName, status)
	}
	standing, err := boot.Describe(ctx, preview)
	if err != nil {
		t.Fatalf("Describe(%s) after destroying its sibling = %v", preview, err)
	}
	if stack := stackNamed(t, standing, previewStackName); !standing.Present || !stack.DigestCurrent {
		t.Errorf("Describe(%s) = %+v after its sibling was destroyed, want a class untouched by a destroy beside it", preview, stack)
	}
	dropped, err := boot.Describe(ctx, production)
	if err != nil {
		t.Fatal(err)
	}
	if dropped.Present {
		t.Errorf("Describe(%s) still claims a bootstrap after the class was destroyed", production)
	}

	last, err := boot.PlanRemove(ctx, preview)
	if err != nil {
		t.Fatalf("PlanRemove(%s) = %v", preview, err)
	}
	alone := changeFor(groupNamed(t, last, "aws/"+bootstrap.ParamGroupName), passphraseParam)
	if alone.Action != providerkit.ActionDelete {
		t.Errorf("destroying the last class plans the passphrase as %q, and a secret nothing decrypts with is one nobody rotates", alone.Action)
	}
	if err := boot.Remove(ctx, preview, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", preview, err)
	}
	if a.paramStands(t, passphraseParam) {
		t.Error("the passphrase stands after the last class on this account went")
	}
}
