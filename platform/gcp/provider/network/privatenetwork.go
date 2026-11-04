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
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	NetworkUserRole         = "roles/compute.networkUser"
	runAgentDomain          = "@serverless-robot-prod.iam.gserviceaccount.com"
	SubnetRange             = "10.240.0.0/20"
	MemorystoreServiceClass = "gcp-memorystore"
	CloudSQLServiceClass    = "google-cloud-sql"
	regionalRouting         = "REGIONAL"
	defaultMTU              = 1460
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

const PartName = "private-network"

var Summary = summaryOf(SubnetRange)

func summaryOf(subnetRange string) string {
	return "a network of the tier's own with one subnetwork, " + subnetRange + ", which Cloud Run's agent may attach services to, and the service connection policies Memorystore and Cloud SQL reach it through: " +
		"what kv stores, databases and the apps bound to them are connected over, with no recurring cost of its own"
}

const (
	KindNetwork          bootstrap.Kind = "compute:network"
	KindSubnetwork       bootstrap.Kind = "compute:subnetwork"
	KindConnectionPolicy bootstrap.Kind = "networkconnectivity:serviceconnectionpolicy"
)

func NewPart(b bootstrap.Engine, tier environment.Tier, patches Patches) bootstrap.Part {
	resources := []provider.PhysicalResource{
		{Kind: string(KindNetwork), ID: b.Clients.Network(tier)},
		{Kind: string(KindSubnetwork), ID: b.Clients.Subnetwork(tier)},
	}
	for _, policy := range connectionPolicies {
		resources = append(resources, provider.PhysicalResource{Kind: string(KindConnectionPolicy), ID: b.Clients.ConnectionPolicy(tier, policy.name)})
	}
	content := SubnetRange + " " + NetworkUserRole
	if digest := patches.Digest(); digest != "" {
		content += " " + digest
	}
	part := bootstrap.Part{
		Name:      PartName,
		Summary:   summaryOf(patches.subnetRange(b, tier)),
		Content:   content,
		APIs:      networkAPIs,
		Resources: resources,
		Inventory: Inventory(b.Clients.Names, tier, b.Clients.Region),
		Make:      func(ctx context.Context) error { return Make(ctx, b, tier, patches) },
		Users:     func(ctx context.Context) ([]string, error) { return listNetworkUsers(ctx, b, tier) },
		RefuseInUse: func(ctx context.Context, removal bootstrap.PartRemoval) error {
			return refuseNetworkInUse(ctx, b, tier, removal)
		},
		Remove: func(ctx context.Context) error { return Remove(ctx, b, tier) },
	}
	if patches.isChanged() {
		part.UpdateReason = "the transforms that patch the network of the " + string(tier) + " tier differ from the ones it records, so this deploy makes it again with them, for every project in the tier"
	}
	return part
}

func Inventory(names names.Names, tier environment.Tier, region string) []pricing.Resource {
	items := []pricing.Resource{
		{Name: names.Network(tier), Type: cost.NetworkType, Properties: map[string]any{"auto_create_subnetworks": false}},
		{Name: names.Subnetwork(tier), Type: cost.SubnetworkType, Properties: map[string]any{"region": region, "ip_cidr_range": SubnetRange}},
	}
	for _, policy := range connectionPolicies {
		items = append(items, pricing.Resource{Name: names.ConnectionPolicy(tier, policy.name), Type: cost.ConnectionPolicyType, Properties: map[string]any{"location": region, "service_class": policy.class}})
	}
	return items
}

func Make(ctx context.Context, b bootstrap.Engine, tier environment.Tier, patches Patches) error {
	patched, err := patches.patch(b, tier)
	if err != nil {
		return err
	}
	engine, err := b.Clients.Compute()
	if err != nil {
		return err
	}
	applied := patched.applied()
	if err := patches.record(ctx, b.Records, tier, joinApplied(patches.recorded.Applied, applied)); err != nil {
		return err
	}
	if err := ensureNetwork(ctx, b, engine, tier, patched.network); err != nil {
		return err
	}
	if err := ensureSubnetwork(ctx, b, engine, tier, patched.subnetwork); err != nil {
		return err
	}
	if err := letCloudRunUseSubnetwork(ctx, b, engine, tier); err != nil {
		return err
	}
	for i, policy := range connectionPolicies {
		if err := ensureConnectionPolicy(ctx, b, tier, policy, patched.policies[i]); err != nil {
			return err
		}
	}
	return patches.record(ctx, b.Records, tier, applied)
}

func desiredNetwork(b bootstrap.Engine, tier environment.Tier) compute.Network {
	return compute.Network{
		Name:                  b.Clients.Network(tier),
		Description:           "the network ocel's " + string(tier) + " kv stores and databases are reached over",
		AutoCreateSubnetworks: false,
		RoutingConfig:         &compute.NetworkRoutingConfig{RoutingMode: regionalRouting},
		ForceSendFields:       []string{"AutoCreateSubnetworks"},
	}
}

func ensureNetwork(ctx context.Context, b bootstrap.Engine, engine *compute.Service, tier environment.Tier, patched surface.Patched[compute.Network, compute.Network]) error {
	name := b.Clients.Network(tier)
	current, err := googleretry.Attempted(ctx, engine.Networks.Get(b.Clients.Project(), name).Context(ctx).Do)
	if err == nil {
		return refuseNewMTU(tier, current, patched)
	}
	if !gcloud.Absent(err) {
		return fmt.Errorf("read the %s network: %w", name, err)
	}
	desired := patched.Desired
	return computeAwaited(ctx, b, engine, "", "create the "+name+" network", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Networks.Insert(b.Clients.Project(), &desired).Context(ctx).Do(call...)
	})
}

func refuseNewMTU(tier environment.Tier, current *compute.Network, patched surface.Patched[compute.Network, compute.Network]) error {
	held := current.Mtu
	if held == 0 {
		held = defaultMTU
	}
	if !patched.Sets("mtu") || patched.Desired.Mtu == held {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s tier's network was created with an MTU of %d, and a transform's `network.mtu` now asks for %d; ocel sets a network's MTU only when it creates it. "+
			"Set it back to %d, or remove everything in the tier's network and `%s --remove %s` first",
		tier, held, patched.Desired.Mtu, held, provider.BootstrapCommand(tier), PartName)
}

func desiredSubnetwork(b bootstrap.Engine, tier environment.Tier) compute.Subnetwork {
	return compute.Subnetwork{
		Name:        b.Clients.Subnetwork(tier),
		Description: "the subnetwork Cloud Run reaches ocel's " + string(tier) + " kv stores and databases from",
		Network:     b.Clients.NetworkPath(tier),
		Region:      b.Clients.Region,
		IpCidrRange: SubnetRange,
	}
}

func ensureSubnetwork(ctx context.Context, b bootstrap.Engine, engine *compute.Service, tier environment.Tier, patched surface.Patched[compute.Subnetwork, subnetworkUpdate]) error {
	project, region, name := b.Clients.Project(), b.Clients.Region, b.Clients.Subnetwork(tier)
	current, err := googleretry.Attempted(ctx, engine.Subnetworks.Get(project, region, name).Context(ctx).Do)
	if gcloud.Absent(err) {
		desired := patched.Desired
		return computeAwaited(ctx, b, engine, region, "create the "+name+" subnetwork", func(call ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.Subnetworks.Insert(project, region, &desired).Context(ctx).Do(call...)
		})
	}
	if err != nil {
		return fmt.Errorf("read the %s subnetwork: %w", name, err)
	}
	if err := refuseNewRange(tier, current, patched); err != nil {
		return err
	}
	var update subnetworkUpdate
	if _, err := patched.Drift(current, &update); err != nil {
		return err
	}
	if on := update.privateIpGoogleAccess; on != nil {
		if err := computeAwaited(ctx, b, engine, region, "set private Google access on the "+name+" subnetwork", func(call ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.Subnetworks.SetPrivateIpGoogleAccess(project, region, name, &compute.SubnetworksSetPrivateIpGoogleAccessRequest{
				PrivateIpGoogleAccess: *on, ForceSendFields: []string{"PrivateIpGoogleAccess"},
			}).Context(ctx).Do(call...)
		}); err != nil {
			return err
		}
	}
	if !update.hasFields() {
		return nil
	}
	if update.privateIpGoogleAccess != nil {
		if current, err = googleretry.Attempted(ctx, engine.Subnetworks.Get(project, region, name).Context(ctx).Do); err != nil {
			return fmt.Errorf("read the %s subnetwork: %w", name, err)
		}
	}
	update.fields.Fingerprint = current.Fingerprint
	return computeAwaited(ctx, b, engine, region, "update the "+name+" subnetwork", func(call ...googleapi.CallOption) (*compute.Operation, error) {
		return engine.Subnetworks.Patch(project, region, name, &update.fields).Context(ctx).Do(call...)
	})
}

func refuseNewRange(tier environment.Tier, current *compute.Subnetwork, patched surface.Patched[compute.Subnetwork, subnetworkUpdate]) error {
	if !patched.Sets("ipCidrRange") || patched.Desired.IpCidrRange == current.IpCidrRange {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s tier's network was created with %s, and a transform's `subnetwork.ipCidrRange` now asks for %s; a subnetwork's range cannot change without replacing it and everything connected through it. "+
			"Set it back to %s, or remove everything in the tier's network and `%s --remove %s` first",
		tier, current.IpCidrRange, patched.Desired.IpCidrRange, current.IpCidrRange, provider.BootstrapCommand(tier), PartName)
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

func desiredPolicy(b bootstrap.Engine, tier environment.Tier, policy connectionPolicy) networkconnectivity.ServiceConnectionPolicy {
	return networkconnectivity.ServiceConnectionPolicy{
		Description:  "lets " + policy.service + " connect ocel's " + string(tier) + " resources into " + b.Clients.Network(tier),
		Network:      b.Clients.NetworkPath(tier),
		ServiceClass: policy.class,
		PscConfig:    &networkconnectivity.PscConfig{Subnetworks: []string{b.Clients.SubnetworkPath(b.Clients.Region, tier)}},
	}
}

func ensureConnectionPolicy(ctx context.Context, b bootstrap.Engine, tier environment.Tier, policy connectionPolicy, patched surface.Patched[networkconnectivity.ServiceConnectionPolicy, policyUpdate]) error {
	service, err := b.Clients.Connectivity()
	if err != nil {
		return err
	}
	policies := service.Projects.Locations.ServiceConnectionPolicies
	named := b.Clients.ConnectionPolicy(tier, policy.name)
	current, err := googleretry.Attempted(ctx, policies.Get(connectionPolicyPath(b, tier, policy)).Context(ctx).Do)
	if err == nil {
		return updateConnectionPolicy(ctx, b, service, current, named, connectionPolicyPath(b, tier, policy), patched)
	}
	if !gcloud.Absent(err) {
		return fmt.Errorf("read the %s service connection policy: %w", named, err)
	}
	desired := patched.Desired
	started, err := googleretry.Attempted(ctx, policies.Create(b.Clients.Location(), &desired).ServiceConnectionPolicyId(named).Context(ctx).Do)
	if gcloud.Taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s service connection policy: %w", named, err)
	}
	return connectivityAwaited(ctx, b, service, "creating the "+named+" service connection policy", started)
}

func updateConnectionPolicy(
	ctx context.Context, b bootstrap.Engine,
	service *networkconnectivity.Service,
	current *networkconnectivity.ServiceConnectionPolicy,
	named, at string,
	patched surface.Patched[networkconnectivity.ServiceConnectionPolicy, policyUpdate],
) error {
	update := newPolicyUpdate(current)
	if _, err := patched.Drift(current, &update); err != nil || len(update.mask) == 0 {
		return err
	}
	update.policy.Etag = current.Etag
	started, err := googleretry.Attempted(ctx, service.Projects.Locations.ServiceConnectionPolicies.Patch(at, &update.policy).
		UpdateMask(strings.Join(update.mask, ",")).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("update the %s service connection policy: %w", named, err)
	}
	return connectivityAwaited(ctx, b, service, "updating the "+named+" service connection policy", started)
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
	own := b.Clients.NetworkPath(tier)
	listOn := func(token string) ([]gcloud.MemorystoreInstance, string, error) {
		listed, next, err := b.Stores.ListInstances(ctx, b.Clients.Location(), filter, resourcesNamedOnNetwork, token)
		return slices.DeleteFunc(listed, func(instance gcloud.MemorystoreInstance) bool {
			return !gcloud.CanBeOn(gcloud.StoreNetworks(&instance), own)
		}), next, err
	}
	connected, next, err := listOn("")
	for page := 1; err == nil && len(connected) == 0 && next != ""; page++ {
		if page == StorePagesRead {
			return nil, fmt.Errorf("read which kv stores are on the %s network: Memorystore answered %d pages with no store on it and named another, so nothing says the network is free", b.Clients.Network(tier), StorePagesRead)
		}
		connected, next, err = listOn(next)
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
		if !gcloud.CanBeOn(gcloud.DatabaseNetworks(instance), b.Clients.NetworkPath(tier)) {
			continue
		}
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
	return forgetPatches(ctx, b.Records, tier)
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

var Part = component.Part{Name: PartName, Inventory: Inventory}
