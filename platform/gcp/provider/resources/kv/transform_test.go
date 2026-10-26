package kv

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

type patching struct {
	patches transform.Patches
	tags    map[string]string
}

func (p *patching) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	encoded, err := json.Marshal(p.patches)
	if err != nil {
		return nil, err
	}
	results := make([]transform.Result, len(req.Resources))
	for i := range results {
		var copied transform.Patches
		if err := json.Unmarshal(encoded, &copied); err != nil {
			return nil, err
		}
		results[i] = transform.Result{Patches: copied, Tags: p.tags}
	}
	return results, nil
}

func instancePatch(patch map[string]any) transform.Patches {
	return transform.Patches{"instance": patch}
}

func placedOn(network string) transform.Patches {
	return instancePatch(map[string]any{"desiredAutoCreatedEndpoints": []any{map[string]any{"network": network}}})
}

type transformedStore struct {
	*KV
	server  *googletest.Memorystore
	records *fake.KeyValues
	in      resources.ProvisionRequest
}

func servingTransformedStore(t *testing.T) *transformedStore {
	t.Helper()
	server, url := googletest.StartMemorystore(t)
	store := fake.NewKeyValues()
	return &transformedStore{
		KV:      New(googletest.Opener(googletest.Clients(url)), store, false),
		server:  server,
		records: store,
		in:      aKVStore(t, provider.KVSpec{MemoryBytes: 256 << 20}),
	}
}

func (s *transformedStore) evaluated(t *testing.T, pass *patching) context.Context {
	t.Helper()
	ref := s.in.Ref
	evaluated, err := surface.Evaluate(context.Background(), pass, surface.Request(ref.Tier, ref.Name.Env, []transform.Resource{{Type: "kv", Name: s.in.Resource.Name}}),
		map[string]surface.Allowed{"kv": transformTarget().Allowed})
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	return surface.WithEvaluated(context.Background(), ref, evaluated)
}

func (s *transformedStore) provision(t *testing.T, pass *patching) (provider.Binding, error) {
	t.Helper()
	return s.Provision(s.evaluated(t, pass), s.in, nil)
}

func (s *transformedStore) mustProvision(t *testing.T, pass *patching) provider.Binding {
	t.Helper()
	binding, err := s.provision(t, pass)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	return binding
}

func (s *transformedStore) plan(t *testing.T, pass *patching) (provider.ChangeAction, string, error) {
	t.Helper()
	masks := len(s.server.Masks())
	action, reason, err := s.Plan(s.evaluated(t, pass), s.in, nil)
	if got := s.server.Masks()[masks:]; len(got) != 0 {
		t.Errorf("Plan() updated %v, want a plan that only reads", got)
	}
	return action, reason, err
}

func refusedNaming(t *testing.T, doing string, err error, named ...string) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("%s = %v, want an %s refusal", doing, err, refusal.CodeInvalid)
		return
	}
	for _, name := range named {
		if !strings.Contains(refused.Message, name) {
			t.Errorf("%s = %v, want the refusal to name %s", doing, err, name)
		}
	}
}

func tunedStore() map[string]any {
	return map[string]any{
		"replicaCount":           2,
		"zoneDistributionConfig": map[string]any{"mode": "SINGLE_ZONE", "zone": "europe-west1-b"},
		"maintenancePolicy": map[string]any{"weeklyMaintenanceWindows": []any{
			map[string]any{"day": "SUNDAY", "startTime": map[string]any{"hours": 3}},
		}},
		"automatedBackupConfig": map[string]any{
			"fixedFrequencySchedule": map[string]any{"startTime": map[string]any{"hours": 2}},
			"retention":              "604800s",
		},
		"deletionProtectionEnabled": true,
	}
}

func TestATransformedStoreIsCreatedWithWhatThePatchSet(t *testing.T) {
	s := servingTransformedStore(t)
	pass := &patching{patches: instancePatch(tunedStore()), tags: map[string]string{"team": "cache"}}

	s.mustProvision(t, pass)

	sent := s.server.Creates()[0]
	if sent.ReplicaCount == nil || *sent.ReplicaCount != 2 {
		t.Errorf("created %v replicas, want the patched two", sent.ReplicaCount)
	}
	if zones := sent.ZoneDistributionConfig; zones == nil || zones.Mode != "SINGLE_ZONE" || zones.Zone != "europe-west1-b" {
		t.Errorf("created zone distribution %s, want the one zone patched", googletest.JSON(t, zones))
	}
	if policy := sent.MaintenancePolicy; policy == nil || len(policy.WeeklyMaintenanceWindow) != 1 || policy.WeeklyMaintenanceWindow[0].Day != "SUNDAY" ||
		policy.WeeklyMaintenanceWindow[0].StartTime == nil || policy.WeeklyMaintenanceWindow[0].StartTime.Hours != 3 {
		t.Errorf("created maintenance policy %s, want Sundays at 03:00", googletest.JSON(t, policy))
	}
	if backups := sent.AutomatedBackupConfig; backups == nil || backups.AutomatedBackupMode != "ENABLED" || backups.Retention != "604800s" ||
		backups.FixedFrequencySchedule == nil || backups.FixedFrequencySchedule.StartTime == nil || backups.FixedFrequencySchedule.StartTime.Hours != 2 {
		t.Errorf("created automated backups %s, want daily backups at 02:00 kept a week", googletest.JSON(t, backups))
	}
	if sent.DeletionProtectionEnabled == nil || !*sent.DeletionProtectionEnabled {
		t.Error("created the instance unprotected, want the patched deletion protection")
	}
	if sent.Labels["team"] != "cache" || sent.Labels["ocel-tier"] != "production" {
		t.Errorf("created labels %v, want the rule's tag beside ocel's own", sent.Labels)
	}
	if sent.PersistenceConfig == nil || sent.PersistenceConfig.Mode != "AOF" || sent.NodeType != "CUSTOM_PICO" {
		t.Errorf("created %s with persistence %s, want ocel's node and append-only file kept", sent.NodeType, googletest.JSON(t, sent.PersistenceConfig))
	}

	s.mustProvision(t, pass)
	if masks := s.server.Masks(); len(masks) != 0 {
		t.Errorf("a second deploy updated %v, want nothing sent over a store already as patched", masks)
	}
}

func TestAPatchAddedToAStoreInPlaceIsPlannedAndSent(t *testing.T) {
	s := servingTransformedStore(t)
	pass := &patching{}
	s.mustProvision(t, pass)

	pass.patches = instancePatch(map[string]any{
		"replicaCount": 2,
		"maintenancePolicy": map[string]any{"weeklyMaintenanceWindows": []any{
			map[string]any{"day": "SUNDAY", "startTime": map[string]any{"hours": 3}},
		}},
	})
	action, reason, err := s.plan(t, pass)
	if err != nil || action != provider.ActionUpdate {
		t.Fatalf("Plan() = %s %q, %v, want %s", action, reason, err, provider.ActionUpdate)
	}
	for _, want := range []string{"replica count changes from 1 to 2", "maintenance policy changes from any window to SUNDAY 03:00"} {
		if !strings.Contains(reason, want) {
			t.Errorf("Plan() reason = %q, want it to say %q", reason, want)
		}
	}

	s.mustProvision(t, pass)
	if masks := s.server.Masks(); !slices.Equal(masks, []string{"replica_count", "maintenance_policy"}) {
		t.Errorf("updated %v, want the replicas alone and then the maintenance policy", masks)
	}
	s.mustProvision(t, pass)
	if masks := s.server.Masks(); len(masks) != 2 {
		t.Errorf("a deploy over the patched store updated %v, want nothing more", masks[2:])
	}
}

func TestRemovingAPatchPutsTheStoreBackToWhatOcelSets(t *testing.T) {
	s := servingTransformedStore(t)
	tuned := tunedStore()
	delete(tuned, "zoneDistributionConfig")
	pass := &patching{patches: instancePatch(tuned), tags: map[string]string{"team": "cache"}}
	s.mustProvision(t, pass)

	pass.patches, pass.tags = nil, nil
	s.mustProvision(t, pass)

	held := s.server.Instance(storeInstance(t))
	if held.ReplicaCount == nil || *held.ReplicaCount != 1 {
		t.Errorf("the store runs %v replicas, want production's one back", held.ReplicaCount)
	}
	if held.MaintenancePolicy != nil {
		t.Errorf("the store keeps maintenance policy %s, want it cleared", googletest.JSON(t, held.MaintenancePolicy))
	}
	if backups := held.AutomatedBackupConfig; backups == nil || backups.AutomatedBackupMode != "DISABLED" {
		t.Errorf("the store keeps automated backups %s, want them off as Google sets them", googletest.JSON(t, backups))
	}
	if held.DeletionProtectionEnabled == nil || *held.DeletionProtectionEnabled {
		t.Error("the store keeps deletion protection on, want it off as ocel sets it")
	}
	if _, kept := held.Labels["team"]; kept || held.Labels["ocel-tier"] != "production" {
		t.Errorf("the store is labelled %v, want the rule's tag gone and ocel's own kept", held.Labels)
	}
	if _, err := s.records.Read(context.Background(), surface.KeyFor(s.in.Ref, "kv", "cache")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("what the transforms patched reads %v, want it forgotten once nothing is patched", err)
	}

	masks := len(s.server.Masks())
	s.mustProvision(t, pass)
	if got := s.server.Masks()[masks:]; len(got) != 0 {
		t.Errorf("a deploy over the store put back updated %v, want nothing", got)
	}
}

func TestAStoresZoneDistributionIsRefusedOnceItsInstanceIsMade(t *testing.T) {
	s := servingTransformedStore(t)
	s.mustProvision(t, &patching{})
	pass := &patching{patches: instancePatch(map[string]any{"zoneDistributionConfig": map[string]any{"mode": "SINGLE_ZONE"}})}

	_, _, planned := s.plan(t, pass)
	_, provisioned := s.provision(t, pass)

	for _, err := range []error{planned, provisioned} {
		refusedNaming(t, "a zone distribution changed over a made store", err, "zoneDistributionConfig", "MULTI_ZONE")
	}
	if masks := s.server.Masks(); len(masks) != 0 {
		t.Errorf("updated %v, want the store left as it is", masks)
	}
}

func TestAPlacedStoreIsConnectedIntoTheNetworkItIsPlacedOn(t *testing.T) {
	s := servingTransformedStore(t)
	s.server.HoldNetwork("shared")

	binding := s.mustProvision(t, &patching{patches: placedOn("shared")})

	sent := s.server.Creates()[0]
	if len(sent.Endpoints) != 1 || len(sent.Endpoints[0].Connections) != 1 || sent.Endpoints[0].Connections[0].PSCAutoConnection == nil ||
		sent.Endpoints[0].Connections[0].PSCAutoConnection.Network != "projects/acme-prod/global/networks/shared" ||
		sent.Endpoints[0].Connections[0].PSCAutoConnection.ProjectID != "acme-prod" {
		t.Errorf("created endpoints %s, want one automatic connection into the placed network", googletest.JSON(t, sent.Endpoints))
	}
	if binding.Properties[provider.PropertyHost] != googletest.StoreAddress {
		t.Errorf("the binding reaches %q, want the endpoint on the placed network", binding.Properties[provider.PropertyHost])
	}
	s.mustProvision(t, &patching{patches: placedOn("projects/acme-prod/global/networks/shared")})
	if masks := s.server.Masks(); len(masks) != 0 {
		t.Errorf("a deploy naming the same network by its path updated %v, want nothing", masks)
	}
}

func TestAStorePlacedOnANetworkTheProjectLacksIsRefusedBeforeAnythingIsMade(t *testing.T) {
	s := servingTransformedStore(t)
	pass := &patching{patches: placedOn("shared")}

	_, _, planned := s.plan(t, pass)
	_, provisioned := s.provision(t, pass)

	for _, err := range []error{planned, provisioned} {
		refusedNaming(t, "a store placed on a missing network", err, "shared", "acme-prod")
	}
	if created := s.server.Creates(); len(created) != 0 {
		t.Errorf("Memorystore created %d instances, want nothing made on a network that is not there", len(created))
	}

	s.skipChecks = true
	if _, err := s.provision(t, pass); err != nil {
		t.Errorf("Provision() with checks skipped = %v, want the network left unread", err)
	}
}

func TestAStorePlacedOnAnotherProjectsNetworkIsRefused(t *testing.T) {
	s := servingTransformedStore(t)

	_, err := s.provision(t, &patching{patches: placedOn("projects/acme-shared/global/networks/shared")})

	refusedNaming(t, "a store placed on another project's network", err, "acme-shared")
	two := instancePatch(map[string]any{"desiredAutoCreatedEndpoints": []any{map[string]any{"network": "a"}, map[string]any{"network": "b"}}})
	if _, err := s.provision(t, &patching{patches: two}); err == nil {
		t.Error("Provision() placing a store on two networks = nil error, want a store given exactly one endpoint")
	}
}

func TestMovingAStoreToAnotherNetworkIsRefusedAtPlanAndProvision(t *testing.T) {
	s := servingTransformedStore(t)
	s.server.HoldNetwork("shared")
	s.mustProvision(t, &patching{})
	pass := &patching{patches: placedOn("shared")}

	_, _, planned := s.plan(t, pass)
	_, provisioned := s.provision(t, pass)

	for _, err := range []error{planned, provisioned} {
		refusedNaming(t, "a store on ocel-prod-network placed on shared", err, "ocel-prod-network", "shared")
	}
	if masks := s.server.Masks(); len(masks) != 0 {
		t.Errorf("updated %v, want the store left on its network", masks)
	}
}

func TestRemovingAProtectedStoreTurnsItsProtectionOffAndForgetsWhatItsTransformsPatched(t *testing.T) {
	s := servingTransformedStore(t)
	binding := s.mustProvision(t, &patching{patches: instancePatch(map[string]any{"deletionProtectionEnabled": true})})

	if err := s.Remove(context.Background(), s.in.Ref, binding, nil); err != nil {
		t.Fatalf("Remove() = %v, want ocel to take down the store it made", err)
	}

	if deleted := s.server.Deletes(); len(deleted) != 1 {
		t.Errorf("Memorystore deleted %v, want the store's instance", deleted)
	}
	if masks := s.server.Masks(); !slices.Equal(masks, []string{"deletion_protection_enabled"}) {
		t.Errorf("updated %v before the delete, want deletion protection turned off", masks)
	}
	if _, err := s.records.Read(context.Background(), surface.KeyFor(s.in.Ref, "kv", "cache")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("what the transforms patched reads %v after removal, want it gone with the store", err)
	}
}
