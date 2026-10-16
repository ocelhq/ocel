package variablestoreserver_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	providercontract "github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

type addedSyncs struct {
	mu     sync.Mutex
	added  []string
	refuse error
}

func (s *addedSyncs) ensure(_ context.Context, tier environment.Tier, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse != nil {
		return s.refuse
	}
	s.added = append(s.added, string(tier)+"/"+project)
	return nil
}

func servedAddingSync(t *testing.T, syncs *addedSyncs) variablestorev1connect.VariableStoreServiceClient {
	t.Helper()
	return servedAddingSyncTo(t, fake.NewProvider(fake.Options{}), syncs)
}

func servedAddingSyncTo(t *testing.T, provider *fake.Provider, syncs *addedSyncs) variablestorev1connect.VariableStoreServiceClient {
	t.Helper()
	return serve(t, &variablestoreserver.Service{CallerNamesEnvSource: true, Source: variablestoreserver.FixedBackend{
		KeyValues:           provider.KeyValues(),
		Cipher:              provider.Cipher(),
		EnsureEnvSourceSync: syncs.ensure,
	}})
}

func TestRegisteringAScheduledEnvSourceAddsItsTiersSyncAsTheRegisteringProject(t *testing.T) {
	syncs := &addedSyncs{}
	variables := servedAddingSync(t, syncs)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)

	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatalf("SyncEnvSource() = %v", err)
	}

	if len(syncs.added) != 1 || syncs.added[0] != "production/"+slug {
		t.Errorf("syncs added %v, want production's, added by %s", syncs.added, slug)
	}
}

func TestAnEnvSourceReadWhereOcelRunsAddsNoSync(t *testing.T) {
	syncs := &addedSyncs{}
	variables := servedAddingSync(t, syncs)
	production := environmentv1.Tier_TIER_PRODUCTION

	exec := execSource(t, &variablestorev1.EnvSourceValue{Cell: &variablestorev1.Cell{Key: "TOKEN"}, Value: "t"})
	if _, err := syncEnvSource(variables, production, exec); err != nil {
		t.Fatalf("SyncEnvSource(exec) = %v", err)
	}
	if _, err := syncEnvSource(variables, production, builtin); err != nil {
		t.Fatalf("SyncEnvSource(builtin) = %v", err)
	}

	if len(syncs.added) != 0 {
		t.Errorf("syncs added %v for env sources nothing reads on a schedule, want none", syncs.added)
	}
}

func TestAScheduledEnvSourceWhoseSyncCannotBeAddedIsNeverRegistered(t *testing.T) {
	syncs := &addedSyncs{refuse: refusal.Refuse(refusal.CodeNotReady, "the production bootstrap has no key to seal a value under")}
	variables := servedAddingSync(t, syncs)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)

	_, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SyncEnvSource() with no sync to add = %v, want the refusal passed on", err)
	}

	described, err := variables.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetEnvSource() != "builtin" {
		t.Errorf("DescribeEnvSource() = %+v, %v, want the tier still builtin", described, err)
	}
}

func TestAScheduledEnvSourceRefusedForAWantedPartRecordsTheWantForTheRegisteringProject(t *testing.T) {
	syncs := &addedSyncs{refuse: providercontract.WantedPartError{
		Part: providercontract.FeatureVariablesKey,
		Err:  refusal.Refuse(refusal.CodeNotReady, "the production bootstrap has no key to seal a value under"),
	}}
	vendor := fake.NewProvider(fake.Options{})
	variables := servedAddingSyncTo(t, vendor, syncs)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)

	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SyncEnvSource() = %v, want the refusal passed on", err)
	}

	wants, err := stackrecords.ListBootstrapWants(context.Background(), vendor.KeyValues(), environment.TierProduction)
	if err != nil || !slices.Equal(wants[providercontract.FeatureVariablesKey], []string{slug}) {
		t.Errorf("ListBootstrapWants() = %v, %v, want %s's want of the variables key, for `ocel bootstrap production` to add", wants, err, slug)
	}
}
