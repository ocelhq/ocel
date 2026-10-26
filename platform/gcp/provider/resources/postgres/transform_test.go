package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
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

func settingsPatch(settings map[string]any) transform.Patches {
	return transform.Patches{"instance": {"settings": settings}}
}

func placedOn(network string) map[string]any {
	return map[string]any{"pscConfigs": []any{map[string]any{"pscAutoConnections": []any{map[string]any{"consumerNetwork": network}}}}}
}

func (h *postgresHarness) evaluated(t *testing.T, pass *patching) context.Context {
	t.Helper()
	ref := h.instances.ref
	evaluated, err := surface.Evaluate(context.Background(), pass, surface.Request(ref.Tier, ref.Name.Env, []transform.Resource{{Type: "postgres", Name: "orders"}}),
		map[string]surface.Allowed{"postgres": transformTarget().Allowed})
	if err != nil {
		t.Fatalf("Evaluate() = %v", err)
	}
	return surface.WithEvaluated(context.Background(), ref, evaluated)
}

func (h *postgresHarness) provisionPatched(t *testing.T, pass *patching) (provider.Binding, error) {
	t.Helper()
	request := aDatabase("17")
	spec, err := readPostgres(request.Resource)
	if err != nil {
		t.Fatal(err)
	}
	return h.instances.provision(h.evaluated(t, pass), request.Resource, spec, nil)
}

func (h *postgresHarness) mustProvisionPatched(t *testing.T, pass *patching) provider.Binding {
	t.Helper()
	binding, err := h.provisionPatched(t, pass)
	if err != nil {
		t.Fatalf("provision() = %v", err)
	}
	return binding
}

func (h *postgresHarness) planPatched(t *testing.T, pass *patching) (provider.ChangeAction, string, error) {
	t.Helper()
	request := aDatabase("17")
	spec, err := readPostgres(request.Resource)
	if err != nil {
		t.Fatal(err)
	}
	written := len(h.server.Wrote())
	action, reason, err := h.instances.plan(h.evaluated(t, pass), request.Resource.Name, spec)
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("plan() wrote %v to Cloud SQL, want a plan that only reads", got)
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

func TestATransformedDatabaseIsCreatedWithWhatThePatchSet(t *testing.T) {
	h := servingPostgres(t)
	pass := &patching{patches: settingsPatch(map[string]any{
		"tier":                "db-custom-2-7680",
		"availabilityType":    "ZONAL",
		"diskSize":            50,
		"diskAutoresizeLimit": 200,
		"databaseFlags":       []any{map[string]any{"name": "log_min_duration_statement", "value": "500"}},
		"maintenanceWindow":   map[string]any{"day": 7, "hour": 3, "updateTrack": "stable"},
		"insightsConfig":      map[string]any{"queryInsightsEnabled": true, "recordClientAddress": true},
		"backupConfiguration": map[string]any{
			"startTime":                   "02:00",
			"backupRetentionSettings":     map[string]any{"retainedBackups": 14},
			"transactionLogRetentionDays": 3,
		},
	}), tags: map[string]string{"team": "data"}}

	h.mustProvisionPatched(t, pass)

	settings := h.server.Created[0].Settings
	if settings.Tier != "db-custom-2-7680" || settings.AvailabilityType != "ZONAL" || settings.DataDiskSizeGb != 50 || settings.StorageAutoResizeLimit != 200 {
		t.Errorf("created %s %s with %d GB growing to %d, want the patched tier, availability and disk", settings.Tier, settings.AvailabilityType, settings.DataDiskSizeGb, settings.StorageAutoResizeLimit)
	}
	if len(settings.DatabaseFlags) != 1 || settings.DatabaseFlags[0].Name != "log_min_duration_statement" || settings.DatabaseFlags[0].Value != "500" {
		t.Errorf("created flags %s, want the patched flag", googletest.JSON(t, settings.DatabaseFlags))
	}
	if window := settings.MaintenanceWindow; window == nil || window.Day != 7 || window.Hour != 3 || window.UpdateTrack != "stable" {
		t.Errorf("created maintenance window %s, want Sundays at 03:00 on the stable track", googletest.JSON(t, window))
	}
	if insights := settings.InsightsConfig; insights == nil || !insights.QueryInsightsEnabled || !insights.RecordClientAddress {
		t.Errorf("created insights %s, want query insights recording client addresses", googletest.JSON(t, insights))
	}
	backups := settings.BackupConfiguration
	if backups.StartTime != "02:00" || backups.BackupRetentionSettings == nil || backups.BackupRetentionSettings.RetainedBackups != 14 ||
		backups.TransactionLogRetentionDays != 3 || !backups.Enabled || !backups.PointInTimeRecoveryEnabled {
		t.Errorf("created backups %s, want the patched schedule and retention beside ocel's daily backups and recovery", googletest.JSON(t, backups))
	}
	if settings.UserLabels["team"] != "data" || settings.UserLabels["ocel-postgres"] != "orders" {
		t.Errorf("created labels %v, want the rule's tag beside ocel's own", settings.UserLabels)
	}

	written := len(h.server.Wrote())
	h.mustProvisionPatched(t, pass)
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("a second deploy wrote %v, want nothing over an instance already as patched", got)
	}
}

func TestAPatchAddedToADatabaseInPlaceIsPlannedAndSentInOnePatch(t *testing.T) {
	h := servingPostgres(t)
	pass := &patching{}
	h.mustProvisionPatched(t, pass)

	pass.patches = settingsPatch(map[string]any{
		"tier":          "db-custom-2-7680",
		"databaseFlags": []any{map[string]any{"name": "log_min_duration_statement", "value": "500"}},
	})
	action, reason, err := h.planPatched(t, pass)
	if err != nil || action != provider.ActionUpdate {
		t.Fatalf("plan() = %s %q, %v, want %s", action, reason, err, provider.ActionUpdate)
	}
	for _, want := range []string{"machine type changes from db-custom-1-3840 to db-custom-2-7680", "database flag list changes from none to log_min_duration_statement=500"} {
		if !strings.Contains(reason, want) {
			t.Errorf("plan() reason = %q, want it to say %q", reason, want)
		}
	}

	h.mustProvisionPatched(t, pass)
	if len(h.server.Patched) != 1 {
		t.Fatalf("Cloud SQL was patched %d times, want the tier and flags sent together", len(h.server.Patched))
	}
	sent := h.server.Patched[0]["settings"].(map[string]any)
	if sent["tier"] != "db-custom-2-7680" || sent["databaseFlags"] == nil {
		t.Errorf("patched %v, want the tier and flags", sent)
	}
	written := len(h.server.Wrote())
	h.mustProvisionPatched(t, pass)
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("a deploy over the patched instance wrote %v, want nothing", got)
	}
}

func TestRemovingAPatchPutsTheDatabaseBackToWhatOcelSets(t *testing.T) {
	h := servingPostgres(t)
	pass := &patching{patches: settingsPatch(map[string]any{
		"tier":                "db-custom-2-7680",
		"diskAutoresizeLimit": 200,
		"databaseFlags":       []any{map[string]any{"name": "log_min_duration_statement", "value": "500"}},
		"maintenanceWindow":   map[string]any{"day": 7, "hour": 3},
		"insightsConfig":      map[string]any{"queryInsightsEnabled": true},
		"dataCacheConfig":     map[string]any{"dataCacheEnabled": true},
		"backupConfiguration": map[string]any{
			"backupRetentionSettings":     map[string]any{"retainedBackups": 30},
			"transactionLogRetentionDays": 3,
		},
	}), tags: map[string]string{"team": "data"}}
	h.mustProvisionPatched(t, pass)
	instance := h.server.Created[0].Name

	pass.patches, pass.tags = nil, nil
	h.mustProvisionPatched(t, pass)

	if len(h.server.Patched) != 1 {
		t.Fatalf("Cloud SQL was patched %d times, want the instance put back once", len(h.server.Patched))
	}
	settings := h.server.Instances[instance].Settings
	if settings.Tier != "db-custom-1-3840" || len(settings.DatabaseFlags) != 0 || settings.StorageAutoResizeLimit != 0 {
		t.Errorf("the instance runs %s with flags %s growing to %d GB, want ocel's tier with no flags and no limit", settings.Tier, googletest.JSON(t, settings.DatabaseFlags), settings.StorageAutoResizeLimit)
	}
	if window := settings.MaintenanceWindow; window != nil && window.Day != 0 {
		t.Errorf("the instance keeps maintenance window %s, want any window", googletest.JSON(t, window))
	}
	if insights := settings.InsightsConfig; insights != nil && insights.QueryInsightsEnabled {
		t.Error("the instance keeps query insights on, want them off as Google sets them")
	}
	if cache := settings.DataCacheConfig; cache != nil && cache.DataCacheEnabled {
		t.Error("the instance keeps its data cache on, want it off as Google sets it")
	}
	if retained := settings.BackupConfiguration.BackupRetentionSettings; retained == nil || retained.RetainedBackups != 7 || settings.BackupConfiguration.TransactionLogRetentionDays != 7 {
		t.Errorf("the instance keeps backups %s, want Google's seven backups and seven days of logs", googletest.JSON(t, settings.BackupConfiguration))
	}
	if _, kept := settings.UserLabels["team"]; kept || settings.UserLabels["ocel-postgres"] != "orders" {
		t.Errorf("the instance is labelled %v, want the rule's tag gone and ocel's own kept", settings.UserLabels)
	}
	if _, err := h.records.Read(context.Background(), surface.KeyFor(h.instances.ref, "postgres", "orders")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("what the transforms patched reads %v, want it forgotten once nothing is patched", err)
	}

	written := len(h.server.Wrote())
	h.mustProvisionPatched(t, pass)
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("a deploy over the instance put back wrote %v, want nothing", got)
	}
}

func TestADatabasesDiskTakesAPatchedSizeAndNeverShrinks(t *testing.T) {
	h := servingPostgres(t)
	h.mustProvisionPatched(t, &patching{patches: settingsPatch(map[string]any{"diskSize": 50})})

	action, reason, err := h.planPatched(t, &patching{patches: settingsPatch(map[string]any{"diskSize": 20})})
	if err != nil || action != provider.ActionKeep {
		t.Errorf("plan() of a smaller disk that grows by itself = %s %q, %v, want %s: the disk only ever holds more than asked", action, reason, err, provider.ActionKeep)
	}

	_, _, err = h.planPatched(t, &patching{patches: settingsPatch(map[string]any{"diskSize": 20, "diskAutoresize": false})})
	refusedNaming(t, "plan() of a smaller fixed disk", err, "disk size", "50", "20")
}

func TestADatabasesDiskTypeIsRefusedOnceItsInstanceIsMade(t *testing.T) {
	h := servingPostgres(t)
	h.mustProvisionPatched(t, &patching{})

	_, _, planned := h.planPatched(t, &patching{patches: settingsPatch(map[string]any{"diskType": "PD_HDD"})})
	_, provisioned := h.provisionPatched(t, &patching{patches: settingsPatch(map[string]any{"diskType": "PD_HDD"})})

	for _, err := range []error{planned, provisioned} {
		refusedNaming(t, "a disk type changed over a made instance", err, "settings.diskType", "PD_SSD")
	}
	if len(h.server.Patched) != 0 {
		t.Errorf("Cloud SQL was patched %v, want the instance left as it is", h.server.Patched)
	}
}

func TestAPatchThatWouldShrinkTheAppsConnectionsIsRefused(t *testing.T) {
	h := servingPostgres(t)

	_, err := h.provisionPatched(t, &patching{patches: settingsPatch(map[string]any{"tier": "db-f1-micro"})})
	refusedNaming(t, "a production database patched onto a shared-core tier", err, "db-f1-micro", "25", "100")

	_, err = h.provisionPatched(t, &patching{patches: settingsPatch(map[string]any{
		"databaseFlags": []any{map[string]any{"name": "max_connections", "value": "500"}},
	})})
	refusedNaming(t, "a database patched with max_connections", err, "max_connections")
	if len(h.server.Created) != 0 {
		t.Errorf("Cloud SQL created %d instances, want nothing made for a refused patch", len(h.server.Created))
	}

	preview := servingPostgresIn(t, previewRef())
	if _, err := preview.provisionPatched(t, &patching{patches: settingsPatch(map[string]any{"tier": "db-g1-small"})}); err != nil {
		t.Errorf("a preview database patched onto db-g1-small = %v, want it made: it takes more connections than the preview tier's", err)
	}
}

func TestAPlacedDatabaseIsGivenItsEndpointOnTheNetworkItIsPlacedOn(t *testing.T) {
	h := servingPostgres(t)
	h.networks["shared"] = true

	binding := h.mustProvisionPatched(t, &patching{patches: settingsPatch(map[string]any{"ipConfiguration": placedOn("shared")})})

	psc := h.server.Created[0].Settings.IpConfiguration.PscConfig
	if len(psc.PscAutoConnections) != 1 || psc.PscAutoConnections[0].ConsumerNetwork != "projects/acme-prod/global/networks/shared" ||
		psc.PscAutoConnections[0].ConsumerProject != "acme-prod" || !psc.PscEnabled {
		t.Errorf("created private service connect %s, want the one endpoint made in the placed network", googletest.JSON(t, psc))
	}
	if binding.Properties[provider.PropertyHost] != googletest.DatabaseAddress {
		t.Errorf("the binding's host is %q, want the endpoint's address on the placed network", binding.Properties[provider.PropertyHost])
	}
	written := len(h.server.Wrote())
	h.mustProvisionPatched(t, &patching{patches: settingsPatch(map[string]any{"ipConfiguration": placedOn("projects/acme-prod/global/networks/shared")})})
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("a deploy naming the same network by its path wrote %v, want nothing", got)
	}
}

func TestAPlacementOnANetworkTheProjectLacksIsRefusedBeforeAnythingIsMade(t *testing.T) {
	h := servingPostgres(t)
	pass := &patching{patches: settingsPatch(map[string]any{"ipConfiguration": placedOn("shared")})}

	_, _, planned := h.planPatched(t, pass)
	_, provisioned := h.provisionPatched(t, pass)

	for _, err := range []error{planned, provisioned} {
		refusedNaming(t, "a database placed on a missing network", err, "shared", "acme-prod")
	}
	if len(h.server.Created) != 0 {
		t.Errorf("Cloud SQL created %d instances, want nothing made on a network that is not there", len(h.server.Created))
	}

	h.instances.skipChecks = true
	if _, err := h.provisionPatched(t, pass); err != nil {
		t.Errorf("provision() with checks skipped = %v, want the network left unread", err)
	}
}

func TestAPlacementOnAnotherProjectsNetworkIsRefused(t *testing.T) {
	h := servingPostgres(t)

	_, err := h.provisionPatched(t, &patching{patches: settingsPatch(map[string]any{"ipConfiguration": placedOn("projects/acme-shared/global/networks/shared")})})

	refusedNaming(t, "a database placed on another project's network", err, "acme-shared")
	for _, many := range []map[string]any{
		{"pscConfigs": []any{map[string]any{"pscAutoConnections": []any{map[string]any{"consumerNetwork": "a"}, map[string]any{"consumerNetwork": "b"}}}}},
		{"pscConfigs": []any{map[string]any{"pscAutoConnections": []any{}}}},
	} {
		if _, err := h.provisionPatched(t, &patching{patches: settingsPatch(map[string]any{"ipConfiguration": many})}); err == nil {
			t.Errorf("provision() placing %v = nil error, want an instance given exactly one endpoint", many)
		}
	}
}

func TestMovingADatabaseToAnotherNetworkIsRefusedAtPlanAndProvision(t *testing.T) {
	h := servingPostgres(t)
	h.networks["shared"] = true
	h.mustProvisionPatched(t, &patching{})
	pass := &patching{patches: settingsPatch(map[string]any{"ipConfiguration": placedOn("shared")})}

	_, _, planned := h.planPatched(t, pass)
	_, provisioned := h.provisionPatched(t, pass)

	for _, err := range []error{planned, provisioned} {
		refusedNaming(t, "a database on ocel-prod-network placed on shared", err, "ocel-prod-network", "shared")
	}
	if len(h.server.Patched) != 0 {
		t.Errorf("Cloud SQL was patched %v, want the instance left on its network", h.server.Patched)
	}
}

func TestRemovingADatabaseForgetsWhatItsTransformsPatched(t *testing.T) {
	h := servingPostgres(t)
	h.mustProvisionPatched(t, &patching{patches: settingsPatch(map[string]any{"tier": "db-custom-2-7680"})})

	if err := h.instances.remove(context.Background(), provider.Binding{Type: provider.BindingPostgres, Name: "orders"}, nil); err != nil {
		t.Fatalf("remove() = %v", err)
	}

	if _, err := h.records.Read(context.Background(), surface.KeyFor(h.instances.ref, "postgres", "orders")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("what the transforms patched reads %v after removal, want it gone with the database", err)
	}
}
