package network

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
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	NetworkUserRole         = "roles/compute.networkUser"
	runAgentDomain          = "@serverless-robot-prod.iam.gserviceaccount.com"
	SubnetRange             = "10.240.0.0/20"
	MemorystoreServiceClass = "gcp-memorystore"
	CloudSQLServiceClass    = "google-cloud-sql"
	regionalRouting         = "REGIONAL"
)

type connectionPolicy struct {
	name    string
	class   string
	service string
}

var connectionPolicies = []connectionPolicy{
	{name: names.MemorystorePolicy, class: MemorystoreServiceClass, service: "Memorystore"},
	{name: names.CloudSQLPolicy, class: CloudSQLServiceClass, service: "Cloud SQL"},
}

var networkAPIs = []string{
	"compute.googleapis.com",
	"memorystore.googleapis.com",
	"sqladmin.googleapis.com",
	"networkconnectivity.googleapis.com",
	"serviceconsumermanagement.googleapis.com",
	"servicenetworking.googleapis.com",
}

const (
	PartName = "private-network"
	Summary  = "a network of the tier's own with one subnetwork, " + SubnetRange + ", which Cloud Run's agent may attach services to, and the service connection policies Memorystore and Cloud SQL reach it through: " +
		"what kv stores, databases and the apps bound to them are connected over, with no recurring cost of its own"

	KindNetwork          bootstrap.Kind = "compute:network"
	KindSubnetwork       bootstrap.Kind = "compute:subnetwork"
	KindConnectionPolicy bootstrap.Kind = "networkconnectivity:serviceconnectionpolicy"
)

func NewPart(b bootstrap.Engine, tier environment.Tier) bootstrap.Part {
	resources := []provider.PhysicalResource{
		{Kind: string(KindNetwork), ID: b.Clients.Network(tier)},
		{Kind: string(KindSubnetwork), ID: b.Clients.Subnetwork(tier)},
	}
	for _, policy := range connectionPolicies {
		resources = append(resources, provider.PhysicalResource{Kind: string(KindConnectionPolicy), ID: b.Clients.ConnectionPolicy(tier, policy.name)})
	}
	return bootstrap.Part{
		Name:      PartName,
		Summary:   Summary,
		Content:   SubnetRange + " " + NetworkUserRole,
		APIs:      networkAPIs,
		Resources: resources,
		Shape:     Shape(b.Clients.Names, tier, b.Clients.Region),
		Make:      func(ctx context.Context) error { return Make(ctx, b, tier) },
		Users:     func(ctx context.Context) ([]string, error) { return listNetworkUsers(ctx, b, tier) },
		RefuseInUse: func(ctx context.Context, removal bootstrap.PartRemoval) error {
			return refuseNetworkInUse(ctx, b, tier, removal)
		},
		Remove: func(ctx context.Context) error { return Remove(ctx, b, tier) },
	}
}

func Shape(names names.Names, tier environment.Tier, region string) []pricing.Shaped {
	shaped := []pricing.Shaped{
		{Name: names.Network(tier), Type: cost.NetworkType, Properties: map[string]any{"auto_create_subnetworks": false}},
		{Name: names.Subnetwork(tier), Type: cost.SubnetworkType, Properties: map[string]any{"region": region, "ip_cidr_range": SubnetRange}},
	}
	for _, policy := range connectionPolicies {
		shaped = append(shaped, pricing.Shaped{Name: names.ConnectionPolicy(tier, policy.name), Type: cost.ConnectionPolicyType, Properties: map[string]any{"location": region, "service_class": policy.class}})
	}
	return shaped
}

func Make(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	engine, err := b.Clients.Compute()
	if err != nil {
		return err
	}
	if err := ensureNetwork(ctx, b, engine, tier); err != nil {
		return err
	}
	if err := ensureSubnetwork(ctx, b, engine, tier); err != nil {
		return err
	}
	if err := letCloudRunUseSubnetwork(ctx, b, engine, tier); err != nil {
		return err
	}
	for _, policy := range connectionPolicies {
		if err := ensureConnectionPolicy(ctx, b, tier, policy); err != nil {
			return err
		}
	}
	return nil
}

func ensureNetwork(ctx context.Context, b bootstrap.Engine, engine *compute.Service, tier environment.Tier) error {
	name := b.Clients.Network(tier)
	_, err := googleretry.Attempted(ctx, engine.Networks.Get(b.Clients.Project(), name).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !gcloud.Absent(err) {
		return fmt.Errorf("read the %s network: %w", name, err)
	}
	return computeAwaited(ctx, b, engine, "", "create the "+name+" network", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Networks.Insert(b.Clients.Project(), &compute.Network{
			Name:                  name,
			Description:           "the network ocel's " + string(tier) + " kv stores and databases are reached over",
			AutoCreateSubnetworks: false,
			RoutingConfig:         &compute.NetworkRoutingConfig{RoutingMode: regionalRouting},
			ForceSendFields:       []string{"AutoCreateSubnetworks"},
		}).Context(ctx).Do(call...)
	})
}

func ensureSubnetwork(ctx context.Context, b bootstrap.Engine, engine *compute.Service, tier environment.Tier) error {
	name := b.Clients.Subnetwork(tier)
	_, err := googleretry.Attempted(ctx, engine.Subnetworks.Get(b.Clients.Project(), b.Clients.Region, name).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !gcloud.Absent(err) {
		return fmt.Errorf("read the %s subnetwork: %w", name, err)
	}
	return computeAwaited(ctx, b, engine, b.Clients.Region, "create the "+name+" subnetwork", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Subnetworks.Insert(b.Clients.Project(), b.Clients.Region, &compute.Subnetwork{
			Name:        name,
			Description: "the subnetwork Cloud Run reaches ocel's " + string(tier) + " kv stores and databases from",
			Network:     b.Clients.NetworkPath(tier),
			Region:      b.Clients.Region,
			IpCidrRange: SubnetRange,
		}).Context(ctx).Do(call...)
	})
}

func letCloudRunUseSubnetwork(ctx context.Context, b bootstrap.Engine, engine *compute.Service, tier environment.Tier) error {
	agent, err := b.Clients.ReadServiceAgent(ctx, runAgentDomain)
	if err != nil {
		return err
	}
	if _, err := policy.Edit(ctx, subnetworkPolicy(b, engine, tier), policy.Grant(NetworkUserRole, agent, nil)); err != nil {
		return fmt.Errorf("let Cloud Run attach services to the %s subnetwork: %w", b.Clients.Subnetwork(tier), err)
	}
	return nil
}

func subnetworkPolicy(b bootstrap.Engine, engine *compute.Service, tier environment.Tier) policy.Resource {
	project, region, name := b.Clients.Project(), b.Clients.Region, b.Clients.Subnetwork(tier)
	return policy.Resource{Name: "the " + name + " subnetwork", Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		held, err := googleretry.Attempted(ctx, engine.Subnetworks.GetIamPolicy(project, region, name).
			OptionsRequestedPolicyVersion(policy.ConditionalVersion).Context(ctx).Do)
		if err != nil {
			return nil, nil, err
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = make([]*compute.Binding, 0, len(bindings))
			for _, binding := range bindings {
				held.Bindings = append(held.Bindings, &compute.Binding{Role: binding.Role, Members: binding.Members, Condition: subnetworkExpr(binding.Condition)})
			}
			_, err := googleretry.Attempted(ctx, engine.Subnetworks.SetIamPolicy(project, region, name,
				&compute.RegionSetPolicyRequest{Policy: held}).Context(ctx).Do)
			return err
		}
		bindings := make([]policy.Binding, 0, len(held.Bindings))
		for _, binding := range held.Bindings {
			bindings = append(bindings, policy.Binding{Role: binding.Role, Members: binding.Members, Condition: subnetworkCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func subnetworkCondition(condition *compute.Expr) *policy.Condition {
	if condition == nil {
		return nil
	}
	return &policy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func subnetworkExpr(condition *policy.Condition) *compute.Expr {
	if condition == nil {
		return nil
	}
	return &compute.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func connectionPolicyPath(b bootstrap.Engine, tier environment.Tier, policy connectionPolicy) string {
	return b.Clients.Location() + "/serviceConnectionPolicies/" + b.Clients.ConnectionPolicy(tier, policy.name)
}

func ensureConnectionPolicy(ctx context.Context, b bootstrap.Engine, tier environment.Tier, policy connectionPolicy) error {
	service, err := b.Clients.Connectivity()
	if err != nil {
		return err
	}
	policies := service.Projects.Locations.ServiceConnectionPolicies
	named := b.Clients.ConnectionPolicy(tier, policy.name)
	_, err = googleretry.Attempted(ctx, policies.Get(connectionPolicyPath(b, tier, policy)).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !gcloud.Absent(err) {
		return fmt.Errorf("read the %s service connection policy: %w", named, err)
	}
	started, err := googleretry.Attempted(ctx, policies.Create(b.Clients.Location(), &networkconnectivity.ServiceConnectionPolicy{
		Description:  "lets " + policy.service + " connect ocel's " + string(tier) + " resources into " + b.Clients.Network(tier),
		Network:      b.Clients.NetworkPath(tier),
		ServiceClass: policy.class,
		PscConfig:    &networkconnectivity.PscConfig{Subnetworks: []string{b.Clients.SubnetworkPath(b.Clients.Region, tier)}},
	}).ServiceConnectionPolicyId(named).Context(ctx).Do)
	if gcloud.Taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s service connection policy: %w", named, err)
	}
	return connectivityAwaited(ctx, b, service, "creating the "+named+" service connection policy", started)
}

const (
	resourcesNamedOnNetwork = 10
	StorePagesRead          = 10
)

type connectedResource struct {
	named   string
	project string
}

func listConnected(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]connectedResource, error) {
	stores, err := listStoresOn(ctx, b, tier)
	if err != nil {
		return nil, err
	}
	databases, err := listDatabasesOn(ctx, b, tier)
	if err != nil {
		return nil, err
	}
	return slices.Concat(stores, databases), nil
}

func listNetworkUsers(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]string, error) {
	connected, err := listConnected(ctx, b, tier)
	if err != nil {
		return nil, err
	}
	users := []string{}
	for _, each := range connected {
		if each.project != "" {
			users = append(users, each.project)
		}
	}
	slices.Sort(users)
	return slices.Compact(users), nil
}

func refuseNetworkInUse(ctx context.Context, b bootstrap.Engine, tier environment.Tier, removal bootstrap.PartRemoval) error {
	connected, err := listConnected(ctx, b, tier)
	if err != nil || len(connected) == 0 {
		return err
	}
	named := make([]string, 0, len(connected))
	for _, each := range connected {
		named = append(named, each.named)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s are still connected to the %s network of tier %s, and taking the network down cuts every app off from them.\n"+
			"Remove them from their projects and deploy them, or destroy their environments, then run `%s` again",
		strings.Join(named, ", "), b.Clients.Network(tier), tier, removal.Retry)
}

func listStoresOn(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]connectedResource, error) {
	filter, err := StoresFilter(b.Clients.Namespace(), tier)
	if err != nil {
		return nil, err
	}
	connected, next, err := b.Stores.ListInstances(ctx, b.Clients.Location(), filter, resourcesNamedOnNetwork, "")
	for page := 1; err == nil && len(connected) == 0 && next != ""; page++ {
		if page == StorePagesRead {
			return nil, fmt.Errorf("read which kv stores are on the %s network: Memorystore answered %d pages with no store and named another, so nothing says the network is free", b.Clients.Network(tier), StorePagesRead)
		}
		connected, next, err = b.Stores.ListInstances(ctx, b.Clients.Location(), filter, resourcesNamedOnNetwork, next)
	}
	if err != nil {
		return nil, fmt.Errorf("read which kv stores are on the %s network: %w", b.Clients.Network(tier), err)
	}
	named := make([]connectedResource, 0, len(connected)+1)
	for _, instance := range connected {
		named = append(named, connectedResource{
			named: fmt.Sprintf("kv %s of project %s environment %s (Memorystore instance %s)",
				instance.Labels[names.KVLabel], instance.Labels[names.ProjectLabel], instance.Labels[names.EnvironmentLabel], path.Base(instance.Name)),
			project: instance.Labels[names.ProjectLabel],
		})
	}
	if next != "" {
		named = append(named, connectedResource{named: "and more"})
	}
	return named, nil
}

func listDatabasesOn(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]connectedResource, error) {
	service, err := b.Clients.SQL()
	if err != nil {
		return nil, err
	}
	filter, err := DatabasesFilter(b.Clients.Namespace(), tier)
	if err != nil {
		return nil, err
	}
	listed, err := googleretry.Attempted(ctx, service.Instances.List(b.Clients.Project()).Filter(filter).
		MaxResults(resourcesNamedOnNetwork).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("read which databases are on the %s network: %w", b.Clients.Network(tier), err)
	}
	named := make([]connectedResource, 0, len(listed.Items)+1)
	for _, instance := range listed.Items {
		labels := map[string]string{}
		if instance.Settings != nil {
			labels = instance.Settings.UserLabels
		}
		named = append(named, connectedResource{
			named: fmt.Sprintf("postgres %s of project %s environment %s (Cloud SQL instance %s)",
				labels[names.PostgresLabel], labels[names.ProjectLabel], labels[names.EnvironmentLabel], instance.Name),
			project: labels[names.ProjectLabel],
		})
	}
	if listed.NextPageToken != "" {
		named = append(named, connectedResource{named: "and more"})
	}
	return named, nil
}

func StoresFilter(namespace provider.Namespace, tier environment.Tier) (string, error) {
	namespaceLabel, err := names.LabelValue(string(namespace))
	if err != nil {
		return "", err
	}
	tierLabel, err := names.LabelValue(string(tier))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("labels.%s=%q AND labels.%s=%q", names.NamespaceLabel, namespaceLabel, names.TierLabel, tierLabel), nil
}

func DatabasesFilter(namespace provider.Namespace, tier environment.Tier) (string, error) {
	namespaceLabel, err := names.LabelValue(string(namespace))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("settings.userLabels.ocel-namespace:%s settings.userLabels.ocel-tier:%s", namespaceLabel, tier), nil
}

func Remove(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	service, err := b.Clients.Connectivity()
	if err != nil {
		return err
	}
	for _, policy := range connectionPolicies {
		named := b.Clients.ConnectionPolicy(tier, policy.name)
		deleting, err := googleretry.Attempted(ctx, service.Projects.Locations.ServiceConnectionPolicies.Delete(connectionPolicyPath(b, tier, policy)).Context(ctx).Do)
		switch {
		case gcloud.Absent(err):
		case err != nil:
			return fmt.Errorf("delete the %s service connection policy: %w", named, err)
		default:
			if err := connectivityAwaited(ctx, b, service, "deleting the "+named+" service connection policy", deleting); err != nil {
				return err
			}
		}
	}
	engine, err := b.Clients.Compute()
	if err != nil {
		return err
	}
	subnetwork := b.Clients.Subnetwork(tier)
	if err := computeAwaited(ctx, b, engine, b.Clients.Region, "delete the "+subnetwork+" subnetwork", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Subnetworks.Delete(b.Clients.Project(), b.Clients.Region, subnetwork).Context(ctx).Do(call...)
	}); err != nil && !gcloud.Absent(err) {
		return err
	}
	network := b.Clients.Network(tier)
	if err := computeAwaited(ctx, b, engine, "", "delete the "+network+" network", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Networks.Delete(b.Clients.Project(), network).Context(ctx).Do(call...)
	}); err != nil && !gcloud.Absent(err) {
		return err
	}
	return nil
}

func computeAwaited(
	ctx context.Context, b bootstrap.Engine,
	engine *compute.Service,
	region, doing string,
	call func(...googleapi.CallOption) (*compute.Operation, error),
) error {
	started, err := googleretry.Attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	finished, err := gcloud.Until(ctx, "Compute Engine to "+doing, func() (*compute.Operation, error) {
		if started.Status == gcloud.OperationDone {
			return started, nil
		}
		if region == "" {
			return googleretry.Attempted(ctx, engine.GlobalOperations.Get(b.Clients.Project(), started.Name).Context(ctx).Do)
		}
		return googleretry.Attempted(ctx, engine.RegionOperations.Get(b.Clients.Project(), region, started.Name).Context(ctx).Do)
	}, func(op *compute.Operation) bool { return op != nil && op.Status == gcloud.OperationDone })
	if err != nil {
		return err
	}
	if finished.Error != nil && len(finished.Error.Errors) > 0 {
		return refusal.Refuse(refusal.CodeNotReady, "Compute Engine refused to %s: %s", doing, finished.Error.Errors[0].Message)
	}
	return nil
}

func connectivityAwaited(ctx context.Context, b bootstrap.Engine, service *networkconnectivity.Service, doing string, started *networkconnectivity.GoogleLongrunningOperation) error {
	_, err := gcloud.AwaitOperation(ctx, gcloud.Patience{Attempts: gcloud.WaitAttempts, Ceiling: gcloud.WaitCeiling}, "Network Connectivity", doing, started, connectivityOutcome,
		func(name string) (*networkconnectivity.GoogleLongrunningOperation, error) {
			return googleretry.Attempted(ctx, service.Projects.Locations.Operations.Get(name).Context(ctx).Do)
		})
	return err
}

func connectivityOutcome(operation *networkconnectivity.GoogleLongrunningOperation) gcloud.OperationOutcome {
	outcome := gcloud.OperationOutcome{Name: operation.Name, Done: operation.Done}
	if operation.Error != nil {
		outcome.Failure = gcloud.FailureOf(operation.Error.Code, operation.Error.Message)
	}
	return outcome
}

var Part = component.Part{Name: PartName, Shape: Shape}
