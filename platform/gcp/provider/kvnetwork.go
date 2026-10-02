package gcp

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/networkconnectivity/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	kvSubnetRange           = "10.240.0.0/20"
	memorystoreServiceClass = "gcp-memorystore"
	networkUserRole         = "roles/compute.networkUser"
	regionalRouting         = "REGIONAL"
	runAgentDomain          = "@serverless-robot-prod.iam.gserviceaccount.com"
)

var kvAPIs = []string{
	"compute.googleapis.com",
	"memorystore.googleapis.com",
	"networkconnectivity.googleapis.com",
	"serviceconsumermanagement.googleapis.com",
}

var kvPermissions = []string{
	"compute.networks.create",
	"compute.networks.get",
	"compute.networks.delete",
	"compute.subnetworks.create",
	"compute.subnetworks.get",
	"compute.subnetworks.delete",
	"compute.subnetworks.getIamPolicy",
	"compute.subnetworks.setIamPolicy",
	"compute.globalOperations.get",
	"compute.regionOperations.get",
	"networkconnectivity.serviceConnectionPolicies.create",
	"networkconnectivity.serviceConnectionPolicies.get",
	"networkconnectivity.serviceConnectionPolicies.delete",
	"networkconnectivity.operations.get",
	"memorystore.instances.list",
	"resourcemanager.projects.get",
}

var kvRoles = []string{"roles/compute.networkAdmin"}

func (b bootstrap) raiseNetwork(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	engine, err := b.clients.Compute()
	if err != nil {
		return err
	}
	if err := b.ensureNetwork(ctx, engine, tier); err != nil {
		return err
	}
	if err := b.ensureSubnetwork(ctx, engine, tier); err != nil {
		return err
	}
	if err := b.grantSubnetworkUse(ctx, engine, tier); err != nil {
		return err
	}
	if err := b.ensureConnectionPolicy(ctx, tier); err != nil {
		return err
	}
	ensureProgress(progress).Debug("The " + string(tier) + " network " + b.clients.Network(tier) + " connects Memorystore through " + kvSubnetRange)
	return nil
}

func (b bootstrap) ensureNetwork(ctx context.Context, engine *compute.Service, tier environment.Tier) error {
	name := b.clients.Network(tier)
	_, err := attempted(ctx, engine.Networks.Get(b.clients.project, name).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !absent(err) {
		return fmt.Errorf("read the %s network: %w", name, err)
	}
	return b.computeAwaited(ctx, engine, "", "create the "+name+" network", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Networks.Insert(b.clients.project, &compute.Network{
			Name:                  name,
			Description:           "the network ocel's " + string(tier) + " kv stores are reached over",
			AutoCreateSubnetworks: false,
			RoutingConfig:         &compute.NetworkRoutingConfig{RoutingMode: regionalRouting},
			ForceSendFields:       []string{"AutoCreateSubnetworks"},
		}).Context(ctx).Do(call...)
	})
}

func (b bootstrap) ensureSubnetwork(ctx context.Context, engine *compute.Service, tier environment.Tier) error {
	name := b.clients.Subnetwork(tier)
	_, err := attempted(ctx, engine.Subnetworks.Get(b.clients.project, b.clients.region, name).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !absent(err) {
		return fmt.Errorf("read the %s subnetwork: %w", name, err)
	}
	return b.computeAwaited(ctx, engine, b.clients.region, "create the "+name+" subnetwork", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Subnetworks.Insert(b.clients.project, b.clients.region, &compute.Subnetwork{
			Name:        name,
			Description: "the subnetwork Cloud Run reaches ocel's " + string(tier) + " kv stores from",
			Network:     b.clients.NetworkPath(tier),
			Region:      b.clients.region,
			IpCidrRange: kvSubnetRange,
		}).Context(ctx).Do(call...)
	})
}

func (b bootstrap) grantSubnetworkUse(ctx context.Context, engine *compute.Service, tier environment.Tier) error {
	agent, err := b.clients.ReadServiceAgent(ctx, runAgentDomain)
	if err != nil {
		return err
	}
	name := b.clients.Subnetwork(tier)
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		policy, err := attempted(ctx, engine.Subnetworks.GetIamPolicy(b.clients.project, b.clients.region, name).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("read who may attach services to the %s subnetwork: %w", name, err)
		}
		if slices.ContainsFunc(policy.Bindings, func(binding *compute.Binding) bool {
			return binding.Role == networkUserRole && binding.Condition == nil && slices.Contains(binding.Members, agent)
		}) {
			return nil
		}
		policy.Bindings = append(policy.Bindings, &compute.Binding{Role: networkUserRole, Members: []string{agent}})
		_, refused = attempted(ctx, engine.Subnetworks.SetIamPolicy(b.clients.project, b.clients.region, name,
			&compute.RegionSetPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil || !stale(refused) {
			break
		}
	}
	if refused != nil {
		return fmt.Errorf("let Cloud Run attach services to the %s subnetwork: %w", name, refused)
	}
	return nil
}

func (b bootstrap) connectionPolicyPath(tier environment.Tier) string {
	return b.clients.location() + "/serviceConnectionPolicies/" + b.clients.ConnectionPolicy(tier)
}

func (b bootstrap) ensureConnectionPolicy(ctx context.Context, tier environment.Tier) error {
	service, err := b.clients.Connectivity()
	if err != nil {
		return err
	}
	policies := service.Projects.Locations.ServiceConnectionPolicies
	_, err = attempted(ctx, policies.Get(b.connectionPolicyPath(tier)).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !absent(err) {
		return fmt.Errorf("read the %s service connection policy: %w", b.clients.ConnectionPolicy(tier), err)
	}
	started, err := attempted(ctx, policies.Create(b.clients.location(), &networkconnectivity.ServiceConnectionPolicy{
		Description:  "lets Memorystore connect ocel's " + string(tier) + " kv stores into " + b.clients.Network(tier),
		Network:      b.clients.NetworkPath(tier),
		ServiceClass: memorystoreServiceClass,
		PscConfig:    &networkconnectivity.PscConfig{Subnetworks: []string{b.clients.SubnetworkPath(b.clients.region, tier)}},
	}).ServiceConnectionPolicyId(b.clients.ConnectionPolicy(tier)).Context(ctx).Do)
	if taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s service connection policy: %w", b.clients.ConnectionPolicy(tier), err)
	}
	return b.connectivityAwaited(ctx, service, "creating the "+b.clients.ConnectionPolicy(tier)+" service connection policy", started)
}

const (
	heldStoresNamed = 10
	storePagesRead  = 10
)

func (b bootstrap) networkFree(ctx context.Context, tier environment.Tier, features []string) error {
	if !slices.Contains(features, kvFeature) {
		return nil
	}
	filter := fmt.Sprintf(`labels.ocel-namespace=%q AND labels.ocel-tier=%q`, naming.Sanitize(string(b.clients.namespace)), string(tier))
	held, next, err := b.stores.listInstances(ctx, b.clients.location(), filter, heldStoresNamed, "")
	for page := 1; err == nil && len(held) == 0 && next != ""; page++ {
		if page == storePagesRead {
			return fmt.Errorf("read which kv stores are on the %s network: Memorystore answered %d pages with no store and named another, so nothing says the network is free", b.clients.Network(tier), storePagesRead)
		}
		held, next, err = b.stores.listInstances(ctx, b.clients.location(), filter, heldStoresNamed, next)
	}
	if err != nil {
		return fmt.Errorf("read which kv stores are on the %s network: %w", b.clients.Network(tier), err)
	}
	if len(held) == 0 {
		return nil
	}
	named := make([]string, 0, len(held))
	for _, instance := range held {
		named = append(named, fmt.Sprintf("kv %s of project %s environment %s (Memorystore instance %s)",
			instance.Labels["ocel-kv"], instance.Labels["ocel-project"], instance.Labels["ocel-environment"], path.Base(instance.Name)))
	}
	if next != "" {
		named = append(named, "and more")
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s network of tier %s still carries %s, and taking the network down cuts every app off from those stores.\n"+
			"Remove those stores from their projects and deploy them, or destroy their environments, then remove feature %s again",
		b.clients.Network(tier), tier, strings.Join(named, ", "), kvFeature)
}

func (b bootstrap) tearNetwork(ctx context.Context, tier environment.Tier) error {
	service, err := b.clients.Connectivity()
	if err != nil {
		return err
	}
	deleting, err := attempted(ctx, service.Projects.Locations.ServiceConnectionPolicies.Delete(b.connectionPolicyPath(tier)).Context(ctx).Do)
	switch {
	case absent(err):
	case err != nil:
		return fmt.Errorf("delete the %s service connection policy: %w", b.clients.ConnectionPolicy(tier), err)
	default:
		if err := b.connectivityAwaited(ctx, service, "deleting the "+b.clients.ConnectionPolicy(tier)+" service connection policy", deleting); err != nil {
			return err
		}
	}
	engine, err := b.clients.Compute()
	if err != nil {
		return err
	}
	subnetwork := b.clients.Subnetwork(tier)
	if err := b.computeAwaited(ctx, engine, b.clients.region, "delete the "+subnetwork+" subnetwork", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Subnetworks.Delete(b.clients.project, b.clients.region, subnetwork).Context(ctx).Do(call...)
	}); err != nil && !absent(err) {
		return err
	}
	network := b.clients.Network(tier)
	if err := b.computeAwaited(ctx, engine, "", "delete the "+network+" network", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Networks.Delete(b.clients.project, network).Context(ctx).Do(call...)
	}); err != nil && !absent(err) {
		return err
	}
	return nil
}

func (b bootstrap) networkInstalled(ctx context.Context, tier environment.Tier) (bool, error) {
	service, err := b.clients.Connectivity()
	if err != nil {
		return false, err
	}
	_, err = attempted(ctx, service.Projects.Locations.ServiceConnectionPolicies.Get(b.connectionPolicyPath(tier)).Context(ctx).Do)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the %s service connection policy: %w", b.clients.ConnectionPolicy(tier), err)
	}
	return true, nil
}

func (b bootstrap) computeAwaited(
	ctx context.Context,
	engine *compute.Service,
	region, doing string,
	call func(...googleapi.CallOption) (*compute.Operation, error),
) error {
	started, err := attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	finished, err := until(ctx, "Compute Engine to "+doing, func() (*compute.Operation, error) {
		if started.Status == operationDone {
			return started, nil
		}
		if region == "" {
			return attempted(ctx, engine.GlobalOperations.Get(b.clients.project, started.Name).Context(ctx).Do)
		}
		return attempted(ctx, engine.RegionOperations.Get(b.clients.project, region, started.Name).Context(ctx).Do)
	}, func(op *compute.Operation) bool { return op != nil && op.Status == operationDone })
	if err != nil {
		return err
	}
	if finished.Error != nil && len(finished.Error.Errors) > 0 {
		return refusal.Refuse(refusal.CodeNotReady, "Compute Engine refused to %s: %s", doing, finished.Error.Errors[0].Message)
	}
	return nil
}

func (b bootstrap) connectivityAwaited(ctx context.Context, service *networkconnectivity.Service, doing string, started *networkconnectivity.GoogleLongrunningOperation) error {
	_, err := awaitOperation(ctx, patience{attempts: waitAttempts, ceiling: waitCeiling}, "Network Connectivity", doing, started, connectivityOutcome,
		func(name string) (*networkconnectivity.GoogleLongrunningOperation, error) {
			return attempted(ctx, service.Projects.Locations.Operations.Get(name).Context(ctx).Do)
		})
	return err
}

func connectivityOutcome(operation *networkconnectivity.GoogleLongrunningOperation) operationOutcome {
	outcome := operationOutcome{name: operation.Name, done: operation.Done}
	if operation.Error != nil {
		outcome.failure = failureOf(operation.Error.Code, operation.Error.Message)
	}
	return outcome
}
