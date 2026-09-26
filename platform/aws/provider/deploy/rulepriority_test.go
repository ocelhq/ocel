package deploy

import (
	"context"
	"errors"
	"strconv"
	"testing"

	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const containerPhysical = "shop-prod-web-container-r3f8a1c90"

func TestARuleKeepsThePriorityItAlreadyHoldsOnTheFront(t *testing.T) {
	t.Parallel()

	hashed := rulePriority(containerPhysical, nil)
	cfg, plan := plannedContainerStack(t)
	cfg.Rules = &fakeRules{held: []elbv2types.Rule{ruleRouting(hashed, containerPhysical), ruleRouting(hashed+1, "blog-prod-web-container-r0000aaaa")}}
	release := releasing(t, cfg)
	work, err := release.containerWork(plan, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if err := release.placeRule(context.Background(), work); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}
	if work.priority != hashed {
		t.Errorf("priority = %d, want %d: the slot is held by this stack's own rule, and a redeploy must not walk away from it", work.priority, hashed)
	}
}

func TestAContainerDeployPlacesItsRuleAgainWhenAnotherDeployClaimsThePriorityFirst(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	cfg.Records = fake.NewRecords()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	rules := &fakeRules{taken: []string{"default"}}
	cfg.Rules = rules
	outputs := substrateOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: containerPhysical,
	}}
	engine := &mockedEngine{outputs: outputs}
	attempts := 0
	engine.upErr = func(stack string) error {
		if stack != plan.Ref.Name.String() {
			return nil
		}
		if attempts++; attempts > 1 {
			return nil
		}
		rules.claim(rulePriority(containerPhysical, nil))
		return errors.New("creating ELBv2 Listener Rule: PriorityInUse: Priority '" + strconv.Itoa(rulePriority(containerPhysical, nil)) + "' is currently in use")
	}
	stacks := standingUp(cfg, engine)

	if _, err := stacks.Provision(context.Background(), plan, edge.DiscardProgress()); err != nil {
		t.Fatalf("Provision() = %v, want the rule placed again at the next free priority", err)
	}
	if attempts != 2 {
		t.Errorf("the app stack ran %d times, want 2: once into the claimed priority and once more past it", attempts)
	}
	if torn := engine.torn(); len(torn) != 0 {
		t.Errorf("a claimed priority tore down %v, want nothing: the deploy recovers rather than abandons", torn)
	}
	if _, present, err := stackrecords.Read(context.Background(), cfg.Records, edge.ClassProduction, SubstrateSlug, substrateRef(edge.ClassProduction).Name); err != nil || !present {
		t.Errorf("the substrate is not recorded (present %v, err %v) after the deploy that recovered", present, err)
	}
}
