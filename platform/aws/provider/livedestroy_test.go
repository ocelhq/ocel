package aws_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestLiveDestroyNamesWhatIsStrandedAndLeavesNothingProvisioned(t *testing.T) {
	a := live(t)
	class := edge.ClassProduction
	boot := a.emptied(t, class)
	ctx := context.Background()

	if err := boot.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: liveWriter}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	deployed, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(a.aws), defaultNamespace, string(class))
	if err != nil {
		t.Fatal(err)
	}

	removal, err := boot.PlanRemove(ctx, class)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	leaving := groupNamed(t, removal, "aws/"+coreStackName)
	if leaving.Action != provider.ActionDelete {
		t.Errorf("PlanRemove() plans %s as %q, want %q", leaving.Name, leaving.Action, provider.ActionDelete)
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
	if passphrase.Action != provider.ActionDelete {
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
		t.Errorf("%s is in state %q after Remove(), want it gone", coreStackName, status)
	}
	for _, bucket := range []string{deployed.StateBucket, deployed.ArtifactBucket, deployed.AssetBucket} {
		if a.bucketExists(t, bucket) {
			t.Errorf("%s still answers after Remove(), and a destroy leaves no bytes behind", bucket)
		}
	}
	for _, table := range []string{deployed.StateTable, deployed.VarsTable} {
		if a.tableExists(t, table) {
			t.Errorf("%s still answers after Remove()", table)
		}
	}
	origin, err := defaultNamespace.OriginSecretParamFor(string(class))
	if err != nil {
		t.Fatal(err)
	}
	for _, param := range []string{origin, passphraseParam} {
		if a.paramExists(t, param) {
			t.Errorf("%s still exists after Remove()", param)
		}
	}

	if err := boot.Remove(ctx, class, nil); err != nil {
		t.Errorf("a second Remove() = %v, want an already-forgotten account to be a no-op", err)
	}

	again, err := boot.Plan(ctx, provider.BootstrapRequest{Class: class, WrittenBy: liveWriter})
	if err != nil {
		t.Fatalf("Plan() after Remove() = %v", err)
	}
	if group := groupNamed(t, again, "aws/"+coreStackName); group.Action != provider.ActionCreate {
		t.Errorf("Plan() over a destroyed account plans %q, want %q", group.Action, provider.ActionCreate)
	}
}

func TestLiveDestroyingOneClassLeavesTheSiblingAndThePassphraseItSharesInPlace(t *testing.T) {
	a := live(t)
	production, preview := edge.ClassProduction, edge.ClassPreview
	boot := a.emptied(t, production, preview)
	ctx := context.Background()

	for _, class := range []edge.Class{production, preview} {
		if err := boot.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: liveWriter}, nil); err != nil {
			t.Fatalf("Apply(%s) = %v", class, err)
		}
	}

	beside, err := boot.PlanRemove(ctx, production)
	if err != nil {
		t.Fatalf("PlanRemove(%s) = %v", production, err)
	}
	shared := changeFor(groupNamed(t, beside, "aws/"+bootstrap.ParamGroupName), passphraseParam)
	if shared.Action != provider.ActionKeep {
		t.Errorf("destroying %s plans the passphrase as %q while %s is still installed on this account", production, shared.Action, preview)
	}
	if !strings.Contains(shared.Reason, string(preview)) {
		t.Errorf("the passphrase is kept with the reason %q, want the sibling that still needs it named", shared.Reason)
	}

	if err := boot.Remove(ctx, production, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", production, err)
	}
	if !a.paramExists(t, passphraseParam) {
		t.Errorf("the passphrase went with %s, and every Pulumi state %s stores is encrypted under it", production, preview)
	}
	if status := a.stackStatus(t, previewStackName); status != "CREATE_COMPLETE" {
		t.Errorf("%s is in state %q after its sibling was destroyed, want CREATE_COMPLETE", previewStackName, status)
	}
	remaining, err := boot.Describe(ctx, preview)
	if err != nil {
		t.Fatalf("Describe(%s) after destroying its sibling = %v", preview, err)
	}
	if stack := stackNamed(t, remaining, previewStackName); !remaining.Present || !stack.DigestCurrent {
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
	if alone.Action != provider.ActionDelete {
		t.Errorf("destroying the last class plans the passphrase as %q, and a secret nothing decrypts with is one nobody rotates", alone.Action)
	}
	if err := boot.Remove(ctx, preview, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", preview, err)
	}
	if a.paramExists(t, passphraseParam) {
		t.Error("the passphrase still exists after the last class on this account went")
	}
}
