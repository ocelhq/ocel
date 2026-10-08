package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const otherEnvironmentLease = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func abandonDeploy(t *testing.T, client contractv1connect.ProviderServiceClient, token string) {
	t.Helper()
	_, err := client.AbandonDeploy(context.Background(), &contractv1.AbandonDeployRequest{
		Slug:        "shop",
		Environment: deployRequest().GetEnvironment(),
		LeaseToken:  token,
	})
	if err != nil {
		t.Fatalf("AbandonDeploy() error = %v", err)
	}
}

func refusedBusy(t *testing.T, result *progressv1.OperationResult, what string) {
	t.Helper()
	if result.GetSuccess() || !strings.Contains(result.GetError(), "another deploy to prod is running") {
		t.Fatalf("%s = %q, want it refused because another deploy holds prod", what, result.GetError())
	}
}

func readEnvironmentLease(t *testing.T, vendor *fake.Provider) (held bool) {
	t.Helper()
	_, err := vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentLeaseKey(environment.TierProduction, "shop", stackrecords.ProductionEnv))
	if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
		t.Fatalf("reading the deploy lease = %v", err)
	}
	return err == nil
}

func takeLeaseOver(t *testing.T, store keyvalue.Store, token string) {
	t.Helper()
	ctx := context.Background()
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, stackrecords.EnvironmentLeaseKey(environment.TierProduction, "shop", stackrecords.ProductionEnv))
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Value, err = json.Marshal(stackrecords.EnvironmentLease{Token: token, ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, recorded); err != nil {
		t.Errorf("taking the lease over = %v", err)
	}
}

func TestProvisionInfraRefusesAnEnvironmentAnotherDeployProvisionedInfraFor(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))

	req := infraRequest(deployRequest())
	req.LeaseToken = otherEnvironmentLease
	result, _ := provisionInfraStream(t, client, req)

	refusedBusy(t, result, "ProvisionInfra()")
}

func TestADeployRefusesAnEnvironmentAnotherDeployProvisionedInfraFor(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	provisioned := len(vendor.FakeStacks().Provisioned())

	result, _ := deploy(t, client, deployRequest())

	refusedBusy(t, result, "Deploy()")
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != provisioned {
		t.Errorf("the refused deploy provisioned %d more stacks, want none: it never held the environment", len(specs)-provisioned)
	}
}

func TestADeployOverTheInfraItsLeaseProvisionedFreesTheEnvironmentWhenItEnds(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	provisionedInfra(t, client, infraRequest(req))

	req.InfraProvisioned, req.LeaseToken = true, infraLease
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed over the lease ProvisionInfra took", result.GetError())
	}

	if readEnvironmentLease(t, vendor) {
		t.Error("the environment still holds a deploy lease after the deploy ended, want it freed")
	}
	next := infraRequest(deployRequest())
	next.LeaseToken = otherEnvironmentLease
	provisionedInfra(t, client, next)
}

func TestARefusedDeployFreesTheEnvironmentItHeld(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	req.InfraProvisioned, req.LeaseToken = true, infraLease

	result, _ := deploy(t, client, req)

	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded over infra that was never provisioned, want it refused")
	}
	if readEnvironmentLease(t, vendor) {
		t.Error("the environment still holds the lease of a deploy that was refused, want it freed")
	}
}

func TestADeployWithNoLeaseHoldsTheEnvironmentOnlyForItsOwnRun(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if readEnvironmentLease(t, vendor) {
		t.Error("the environment still holds a deploy lease after a deploy that brought none ended, want it freed")
	}
}

func TestADeployOverProvisionedInfraRefusesToRunWithoutTheLeaseThatProvisionedIt(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	req := deployRequest()
	provisionedInfra(t, client, infraRequest(req))

	req.InfraProvisioned = true
	_, _, err := deployStream(t, client, req)

	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid {
		t.Fatalf("Deploy() = %v, want it refused as invalid: only the deploy whose lease token provisioned the infra may deploy over it", err)
	}
}

func TestProvisionInfraRefusesARequestWithNoLeaseToken(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := infraRequest(deployRequest())
	req.LeaseToken = ""
	_, err := provisionInfraStream(t, client, req)

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ProvisionInfra() = %v, want it rejected as invalid: nothing could name the lease that holds the environment", err)
	}
	if readEnvironmentLease(t, vendor) {
		t.Error("the rejected ProvisionInfra took a lease")
	}
}

func TestAbandonDeployFreesTheEnvironmentItsLeaseHeld(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))

	abandonDeploy(t, client, infraLease)

	if readEnvironmentLease(t, vendor) {
		t.Error("the environment still holds the abandoned deploy's lease, want it freed")
	}
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Errorf("Deploy() = %q, want it to succeed once the deploy that held the environment was abandoned", result.GetError())
	}
}

func TestAbandonDeployKeepsALeaseAnotherDeployHolds(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))

	abandonDeploy(t, client, otherEnvironmentLease)

	if !readEnvironmentLease(t, vendor) {
		t.Error("abandoning a lease it never held freed the lease of the deploy that holds the environment")
	}
}

func TestAbandonDeployOfALeaseNothingHoldsSucceeds(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	abandonDeploy(t, client, infraLease)
}

func TestProvisionInfraFreesTheEnvironmentWhenItFails(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")

	result, _ := provisionInfraStream(t, client, infraRequest(deployRequest()))

	if result.GetSuccess() {
		t.Fatal("ProvisionInfra() succeeded on a provider that was never bootstrapped, want it refused")
	}
	if readEnvironmentLease(t, vendor) {
		t.Error("the environment still holds the lease of a ProvisionInfra that failed, want it freed")
	}
}

func TestAFailedProvisionInfraKeepsTheLeaseAnEarlierProvisionInfraOfItsDeployTook(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.Kind == provider.StackInfra {
			return errors.New("the infra stack failed to provision")
		}
		return nil
	})

	result, _ := provisionInfraStream(t, client, infraRequest(deployRequest()))

	if result.GetSuccess() {
		t.Fatal("ProvisionInfra() succeeded, want the failed provisioning reported")
	}
	if !readEnvironmentLease(t, vendor) {
		t.Error("the failed ProvisionInfra freed the lease its deploy took earlier, want it held until that deploy ends")
	}
}

func TestADeployWhoseLeaseAnotherDeployTookOverIsRefusedBeforeItPromotes(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	provisionedInfra(t, client, infraRequest(req))
	var takeOver sync.Once
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.Kind != provider.StackApp {
			return nil
		}
		takeOver.Do(func() {
			takeLeaseOver(t, vendor.KeyValues(), otherEnvironmentLease)
		})
		return nil
	})

	req.InfraProvisioned, req.LeaseToken = true, infraLease
	result, events := deploy(t, client, req)

	refusedBusy(t, result, "Deploy()")
	if _, promoted := spanStatuses(events)[promotionSpan]; promoted {
		t.Error("the deploy promoted after another deploy took its lease over, want it stopped before promotion")
	}
}

func TestADryDeployNeverWaitsOnTheDeployHoldingTheEnvironment(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))

	req := deployRequest()
	req.Dry = true
	result, _ := deploy(t, client, req)

	if strings.Contains(result.GetError(), "another deploy") {
		t.Fatalf("Deploy() = %q, want a dry deploy planned beside the deploy that holds the environment: it writes nothing", result.GetError())
	}
}

func TestADeployWhoseLeaseAnotherDeployTookOverAndFreedIsRefusedRatherThanTakingItBack(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	provisionedInfra(t, client, infraRequest(req))
	takeLeaseOver(t, vendor.KeyValues(), otherEnvironmentLease)
	if err := stackrecords.ForgetEnvironmentLease(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop", stackrecords.ProductionEnv, otherEnvironmentLease); err != nil {
		t.Fatal(err)
	}
	provisioned := len(vendor.FakeStacks().Provisioned())

	req.InfraProvisioned, req.LeaseToken = true, infraLease
	result, _ := deploy(t, client, req)

	if result.GetSuccess() || !strings.Contains(result.GetError(), "holds no lease on prod") {
		t.Fatalf("Deploy() = %q, want it refused: the lease that provisioned its infra was taken over and freed, so the infra may not be what it provisioned", result.GetError())
	}
	if specs := vendor.FakeStacks().Provisioned(); len(specs) != provisioned {
		t.Errorf("the refused deploy provisioned %d more stacks, want none", len(specs)-provisioned)
	}
	if readEnvironmentLease(t, vendor) {
		t.Error("the refused deploy took the freed lease back, want the environment left free")
	}
}

func renewDeployLease(client contractv1connect.ProviderServiceClient, token string) error {
	_, err := client.RenewDeployLease(context.Background(), &contractv1.RenewDeployLeaseRequest{
		Slug:        "shop",
		Environment: deployRequest().GetEnvironment(),
		LeaseToken:  token,
	})
	return err
}

func readLeaseRevision(t *testing.T, vendor *fake.Provider) keyvalue.Revision {
	t.Helper()
	recorded, err := vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentLeaseKey(environment.TierProduction, "shop", stackrecords.ProductionEnv))
	if err != nil {
		t.Fatalf("reading the environment lease = %v", err)
	}
	return recorded.Revision
}

func TestRenewDeployLeaseKeepsTheLeaseOfADeployThatIsStillBuilding(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	provisioned := readLeaseRevision(t, vendor)

	if err := renewDeployLease(client, infraLease); err != nil {
		t.Fatalf("RenewDeployLease() error = %v", err)
	}

	if readLeaseRevision(t, vendor) == provisioned {
		t.Error("RenewDeployLease left the lease as ProvisionInfra wrote it, want it renewed")
	}
}

func TestRenewDeployLeaseIsRefusedOnceAnotherDeployTookTheLeaseOver(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	takeLeaseOver(t, vendor.KeyValues(), otherEnvironmentLease)

	err := renewDeployLease(client, infraLease)

	if code, _ := provider.RefusedCode(err); code != refusal.CodeBusy {
		t.Errorf("RenewDeployLease() = %v, want a busy refusal: another deploy holds the lease now", err)
	}
}

func TestAbandonDeployReportsALeaseItCouldNotFree(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	provisionedInfra(t, client, infraRequest(deployRequest()))
	vendor.KeyValues().(*fake.KeyValues).SetRemovalError(
		stackrecords.EnvironmentLeaseKey(environment.TierProduction, "shop", stackrecords.ProductionEnv), errors.New("the table refused the delete"))

	_, err := client.AbandonDeploy(context.Background(), &contractv1.AbandonDeployRequest{
		Slug:        "shop",
		Environment: deployRequest().GetEnvironment(),
		LeaseToken:  infraLease,
	})

	if err == nil {
		t.Error("AbandonDeploy() succeeded though the lease is still recorded, want the failure reported")
	}
}
