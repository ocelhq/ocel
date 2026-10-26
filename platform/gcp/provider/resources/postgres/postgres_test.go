package postgres

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"testing"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type postgresHarness struct {
	server    *googletest.CloudSQL
	instances postgresInstances
	records   *fake.KeyValues
	networks  map[string]bool
}

func (h *postgresHarness) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/global/networks/") {
		h.server.Serve(w, r)
		return
	}
	if !h.networks[path.Base(r.URL.Path)] {
		googletest.WriteNotFound(w)
		return
	}
	googletest.WriteBody(w, map[string]any{"name": path.Base(r.URL.Path)})
}

func servingPostgres(t *testing.T) *postgresHarness {
	t.Helper()
	return servingPostgresIn(t, googletest.ProductionRef(t))
}

func previewRef() provider.StackRef {
	return provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}
}

func servingPostgresIn(t *testing.T, ref provider.StackRef) *postgresHarness {
	t.Helper()
	h := &postgresHarness{server: googletest.NewCloudSQL(), records: fake.NewKeyValues(), networks: map[string]bool{}}
	served := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(served.Close)
	clients := &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Region: "europe-west1", Endpoint: served.URL}
	sql, err := clients.SQL()
	if err != nil {
		t.Fatal(err)
	}
	h.instances = postgresInstances{
		clients: clients,
		sql:     sql,
		records: h.records,
		cipher:  fake.NewCipher(),
		ref:     ref,
	}
	return h
}

func aDatabase(version string) resources.ProvisionRequest {
	return resources.ProvisionRequest{
		Resource: provider.Resource{Name: "orders", Declared: "orders", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{Version: version}},
	}
}

func (h *postgresHarness) provision(t *testing.T, version string) provider.Binding {
	t.Helper()
	request := aDatabase(version)
	spec, err := readPostgres(request.Resource)
	if err != nil {
		t.Fatalf("readPostgres() = %v", err)
	}
	binding, err := h.instances.provision(context.Background(), request.Resource, spec, nil)
	if err != nil {
		t.Fatalf("provision() = %v", err)
	}
	return binding
}

func TestADatabaseIsOneCloudSQLInstanceReachedPrivatelyOverTheTiersNetwork(t *testing.T) {
	h := servingPostgres(t)

	binding := h.provision(t, "16")

	if len(h.server.Created) != 1 {
		t.Fatalf("Cloud SQL was asked to create %d instances, want one per database", len(h.server.Created))
	}
	sent := h.server.Created[0]
	settings := sent.Settings
	if sent.DatabaseVersion != "POSTGRES_16" || sent.Region != "europe-west1" {
		t.Errorf("created %s in %s, want POSTGRES_16 in the deploy's region", sent.DatabaseVersion, sent.Region)
	}
	if settings.Edition != "ENTERPRISE" {
		t.Errorf("created a %s instance, want the Enterprise edition", settings.Edition)
	}
	if settings.DataDiskType != "PD_SSD" || settings.DataDiskSizeGb != 10 || settings.StorageAutoResize == nil || !*settings.StorageAutoResize {
		t.Errorf("created disk %s of %d GB (auto-resize %v), want 10 GB of SSD that grows as it fills", settings.DataDiskType, settings.DataDiskSizeGb, settings.StorageAutoResize)
	}
	if settings.BackupConfiguration == nil || !settings.BackupConfiguration.Enabled {
		t.Errorf("created backups %+v, want daily backups on", settings.BackupConfiguration)
	}
	asked := h.server.Sent[0]["settings"].(map[string]any)
	if public, sent := asked["ipConfiguration"].(map[string]any)["ipv4Enabled"]; !sent || public != false {
		t.Error("created the instance without turning its public address off, and Cloud SQL gives one by default")
	}
	ip := settings.IpConfiguration
	if ip.SslMode != "ENCRYPTED_ONLY" {
		t.Errorf("created ssl mode %q, want a connection refused unless it is encrypted", ip.SslMode)
	}
	psc := ip.PscConfig
	if psc == nil || !psc.PscEnabled || !slices.Equal(psc.AllowedConsumerProjects, []string{"acme-prod"}) ||
		len(psc.PscAutoConnections) != 1 || psc.PscAutoConnections[0].ConsumerNetwork != "projects/acme-prod/global/networks/ocel-prod-network" ||
		psc.PscAutoConnections[0].ConsumerProject != "acme-prod" {
		t.Errorf("created private service connect %s, want an endpoint made in the tier's network", googletest.JSON(t, psc))
	}
	if settings.UserLabels["ocel-postgres"] != "orders" || settings.UserLabels["ocel-tier"] != "production" || settings.UserLabels["ocel-environment"] != "prod" {
		t.Errorf("created labels %v, want the tier, environment and database it serves", settings.UserLabels)
	}
	if tag, named := strings.CutPrefix(sent.Name, "ocel-app-shop-prod-orders-"); !named || !naming.IsRandomTag(tag) {
		t.Errorf("created instance %q, want it named under the app fence for the project, environment and database, with a random tag last", sent.Name)
	}

	instance := sent.Name
	if !slices.Equal(h.server.Databases[instance], []string{"ocel"}) {
		t.Errorf("the instance holds databases %v, want the one the binding names", h.server.Databases[instance])
	}
	password := h.server.Users[instance]["ocel"]
	if len(password) < 32 {
		t.Errorf("the ocel user was given password %q, want a long random one", password)
	}
	want := map[string]string{
		provider.PropertyHost: googletest.DatabaseAddress, provider.PropertyPort: "5432", provider.PropertyDatabase: "ocel",
		provider.PropertyUsername: "ocel", provider.PropertyPassword: password, provider.PropertyTLSMode: "POSTGRES_TLS_MODE_REQUIRE",
	}
	for name, value := range want {
		if binding.Properties[name] != value {
			t.Errorf("the binding's %s is %q, want %q", name, binding.Properties[name], value)
		}
	}
	if err := provider.VerifyProperties(binding); err != nil {
		t.Errorf("VerifyProperties() = %v, want a binding every SDK can connect with", err)
	}
}

func TestAProductionDatabaseRunsOnADedicatedCoreRegionalInstanceWithPointInTimeRecovery(t *testing.T) {
	h := servingPostgres(t)

	h.provision(t, "17")

	settings := h.server.Created[0].Settings
	if settings.Tier != "db-custom-1-3840" || settings.AvailabilityType != "REGIONAL" {
		t.Errorf("created a %s %s instance, want the dedicated-core db-custom-1-3840 with a standby in a second zone: a shared-core one has no SLA", settings.Tier, settings.AvailabilityType)
	}
	if backups := settings.BackupConfiguration; backups == nil || !backups.Enabled || !backups.PointInTimeRecoveryEnabled {
		t.Errorf("created backups %+v, want daily backups with point-in-time recovery", backups)
	}
	if !settings.DeletionProtectionEnabled {
		t.Error("created the instance with deletion protection off, and one call outside ocel would take its data with it")
	}
}

func TestAProductionDatabaseMadeSmallerThanItsTierIsPatchedToTheTiersSize(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	instance := h.server.Created[0].Name
	settings := h.server.Instances[instance].Settings
	settings.Tier, settings.AvailabilityType, settings.DeletionProtectionEnabled = "db-f1-micro", "ZONAL", false
	settings.BackupConfiguration = &sqladmin.BackupConfiguration{Enabled: true}

	h.provision(t, "17")

	if len(h.server.Patched) != 1 {
		t.Fatalf("Cloud SQL was patched %d times, want once to bring the instance to its tier's size", len(h.server.Patched))
	}
	converged := h.server.Instances[instance].Settings
	if converged.Tier != "db-custom-1-3840" || converged.AvailabilityType != "REGIONAL" || !converged.DeletionProtectionEnabled ||
		converged.BackupConfiguration == nil || !converged.BackupConfiguration.Enabled || !converged.BackupConfiguration.PointInTimeRecoveryEnabled {
		t.Errorf("the instance runs as %s, want the production size", googletest.JSON(t, converged))
	}
	written := len(h.server.Wrote())
	h.provision(t, "17")
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("a deploy over an instance already at its tier's size wrote %v, want nothing", got)
	}
}

func TestAPreviewDatabaseRunsOnASharedCoreZonalInstanceADestroyTakesDown(t *testing.T) {
	h := servingPostgresIn(t, previewRef())

	h.provision(t, "17")

	settings := h.server.Created[0].Settings
	if settings.Tier != "db-f1-micro" || settings.AvailabilityType != "ZONAL" {
		t.Errorf("created a %s %s instance, want the shared-core db-f1-micro in one zone", settings.Tier, settings.AvailabilityType)
	}
	asked := h.server.Sent[0]["settings"].(map[string]any)
	if recovers, sent := asked["backupConfiguration"].(map[string]any)["pointInTimeRecoveryEnabled"]; !sent || recovers != false {
		t.Error("created the instance without turning point-in-time recovery off, and a preview would pay for its logs")
	}
	if protected, sent := asked["deletionProtectionEnabled"]; !sent || protected != false {
		t.Error("created the instance without turning deletion protection off")
	}
}

func TestADatabaseOnTheEmulatorIsBoundToTheAddressItsDataPlaneAnswersOn(t *testing.T) {
	h := servingPostgres(t)
	h.server.Addressed, h.server.Primary = false, "172.17.0.3"

	binding := h.provision(t, "16")

	if host := binding.Properties[provider.PropertyHost]; host != "172.17.0.3" {
		t.Errorf("the binding's host is %q, want 172.17.0.3: the emulator makes no private service connect endpoint, "+
			"and its data plane answers on the instance's primary address", host)
	}
}

func TestOnlyTheEmulatorBindsADatabaseToItsPrimaryAddress(t *testing.T) {
	t.Parallel()

	network := "projects/acme-prod/global/networks/ocel-prod-network"
	instance := &sqladmin.DatabaseInstance{
		IpAddresses: []*sqladmin.IpMapping{{Type: "PRIMARY", IpAddress: "34.1.2.3"}},
		Settings: &sqladmin.Settings{IpConfiguration: &sqladmin.IpConfiguration{PscConfig: &sqladmin.PscConfig{
			PscAutoConnections: []*sqladmin.PscAutoConnectionConfig{{ConsumerNetwork: network}},
		}}},
	}

	if got := addressOf(instance, network, false); got != "" {
		t.Errorf("addressOf() on Google Cloud = %q, want none until the tier's network has its endpoint", got)
	}
	if got := addressOf(instance, network, true); got != "34.1.2.3" {
		t.Errorf("addressOf() on the emulator = %q, want the primary address its data plane answers on", got)
	}
	instance.Settings.IpConfiguration.PscConfig.PscAutoConnections[0].IpAddress = "10.240.0.9"
	if got := addressOf(instance, network, true); got != "10.240.0.9" {
		t.Errorf("addressOf() with an endpoint in the tier's network = %q, want that endpoint", got)
	}
}

func TestADatabaseAlreadyInPlaceIsBoundWithTheSamePasswordAndNothingWritten(t *testing.T) {
	h := servingPostgres(t)
	first := h.provision(t, "17")
	written := len(h.server.Wrote())

	second := h.provision(t, "17")

	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("a second deploy wrote %v, want nothing over a database already as declared", got)
	}
	if second.Properties[provider.PropertyPassword] != first.Properties[provider.PropertyPassword] {
		t.Error("a second deploy bound a different password, and the revision still serving holds the first")
	}
	if filters := h.server.Filters(); len(filters) == 0 || !strings.Contains(filters[0], "settings.userLabels.ocel-postgres:orders") ||
		!strings.Contains(filters[0], "settings.userLabels.ocel-environment:prod") || !strings.Contains(filters[0], "settings.userLabels.ocel-namespace:ocel") {
		t.Errorf("the instance was found with filters %q, want one naming this namespace's environment and database", filters)
	}
}

func TestADatabaseWhosePasswordWasLostIsGivenANewOne(t *testing.T) {
	h := servingPostgres(t)
	first := h.provision(t, "17")
	if err := records.Forget(context.Background(), h.records, h.instances.passwordKey("orders")); err != nil {
		t.Fatal(err)
	}

	second := h.provision(t, "17")

	instance := h.server.Created[0].Name
	if second.Properties[provider.PropertyPassword] == first.Properties[provider.PropertyPassword] ||
		h.server.Users[instance]["ocel"] != second.Properties[provider.PropertyPassword] {
		t.Errorf("after the recorded password was lost the user holds %q and the binding names %q, want a new password set on the user and bound",
			h.server.Users[instance]["ocel"], second.Properties[provider.PropertyPassword])
	}
}

func TestAPasswordCloudSQLRefusedToSetIsSetByTheNextDeploy(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	if err := records.Forget(context.Background(), h.records, h.instances.passwordKey("orders")); err != nil {
		t.Fatal(err)
	}
	h.server.RefusedSet = 1
	request := aDatabase("17")
	spec, _ := readPostgres(request.Resource)
	if _, err := h.instances.provision(context.Background(), request.Resource, spec, nil); err == nil {
		t.Fatal("provision() = nil error while Cloud SQL refused the password, want the refusal")
	}

	bound := h.provision(t, "17")

	instance := h.server.Created[0].Name
	if h.server.Users[instance]["ocel"] != bound.Properties[provider.PropertyPassword] {
		t.Errorf("the user holds %q and the binding names %q, want the recorded password set on the user: the app could never sign in",
			h.server.Users[instance]["ocel"], bound.Properties[provider.PropertyPassword])
	}
	written := len(h.server.Wrote())
	h.provision(t, "17")
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("a deploy after the password was set wrote %v, want nothing", got)
	}
}

func TestADatabaseAskingForAnotherMajorVersionThanItRunsIsRefused(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "16")

	spec, err := readPostgres(aDatabase("17").Resource)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.instances.provision(context.Background(), aDatabase("17").Resource, spec, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "16") || !strings.Contains(err.Error(), "17") {
		t.Errorf("provision() of version 17 over an instance running 16 = %v, want an %s refusal naming both", err, refusal.CodeInvalid)
	}
}

func TestADatabaseOnAVersionCloudSQLDoesNotRunIsRefused(t *testing.T) {
	t.Parallel()

	_, err := readPostgres(aDatabase("9").Resource)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "orders") {
		t.Errorf("readPostgres() of version 9 = %v, want an %s refusal naming the database", err, refusal.CodeInvalid)
	}
	if spec, err := readPostgres(provider.Resource{Name: "orders", Type: provider.BindingPostgres}); err != nil || spec.version != "POSTGRES_17" {
		t.Errorf("readPostgres() with no version = %+v, %v, want POSTGRES_17, the SDK's default", spec, err)
	}
}

func TestRemovingADatabaseDeletesItsInstanceAndForgetsItsPassword(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	instance := h.server.Created[0].Name

	if err := h.instances.remove(context.Background(), provider.Binding{Type: provider.BindingPostgres, Name: "orders"}, nil); err != nil {
		t.Fatalf("remove() = %v", err)
	}
	if !slices.Equal(h.server.Deleted, []string{instance}) {
		t.Errorf("Cloud SQL deleted %v, want %s", h.server.Deleted, instance)
	}
	if _, err := h.records.Read(context.Background(), h.instances.passwordKey("orders")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("the recorded password reads %v after removal, want it gone with the database", err)
	}
	if err := h.instances.remove(context.Background(), provider.Binding{Type: provider.BindingPostgres, Name: "orders"}, nil); err != nil {
		t.Errorf("remove() of a database already gone = %v, want nothing to do", err)
	}

	h.provision(t, "17")
	if again := h.server.Created[1].Name; again == instance {
		t.Errorf("the database was created again as %q, a name Cloud SQL holds for a week after the instance is deleted", again)
	}
}

func TestRemovingAProductionDatabaseTurnsOffItsDeletionProtectionBeforeDeletingIt(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	instance := h.server.Created[0].Name

	if err := h.instances.remove(context.Background(), provider.Binding{Type: provider.BindingPostgres, Name: "orders"}, nil); err != nil {
		t.Fatalf("remove() = %v, want ocel to take down the instance it made", err)
	}
	if !slices.Equal(h.server.Deleted, []string{instance}) {
		t.Errorf("Cloud SQL deleted %v, want %s", h.server.Deleted, instance)
	}
	if want := []string{"patch instance " + instance, "delete instance " + instance}; !slices.Equal(h.server.Wrote()[len(h.server.Wrote())-2:], want) {
		t.Errorf("the removal wrote %v, want deletion protection turned off and then the delete", h.server.Wrote())
	}
}

func (h *postgresHarness) plan(t *testing.T, version string) (provider.ChangeAction, string, error) {
	t.Helper()
	request := aDatabase(version)
	spec, err := readPostgres(request.Resource)
	if err != nil {
		t.Fatalf("readPostgres() = %v", err)
	}
	written := len(h.server.Wrote())
	recorded, readErr := h.records.Read(context.Background(), h.instances.passwordKey("orders"))
	action, reason, err := h.instances.plan(context.Background(), request.Resource.Name, spec)
	if got := h.server.Wrote()[written:]; len(got) != 0 {
		t.Errorf("plan() wrote %v to Cloud SQL, want a plan that only reads", got)
	}
	after, afterErr := h.records.Read(context.Background(), h.instances.passwordKey("orders"))
	if !errors.Is(afterErr, readErr) || string(after.Value) != string(recorded.Value) {
		t.Error("plan() changed the password recorded for the database, want a plan that only reads")
	}
	return action, reason, err
}

func TestAMissingDatabasePlansACreate(t *testing.T) {
	h := servingPostgres(t)

	action, _, err := h.plan(t, "17")
	if err != nil || action != provider.ActionCreate {
		t.Errorf("plan() = %s, %v, want %s", action, err, provider.ActionCreate)
	}
}

func TestADatabaseAsDeclaredPlansKeep(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")

	action, reason, err := h.plan(t, "17")
	if err != nil || action != provider.ActionKeep || reason != provider.ReasonCurrent {
		t.Errorf("plan() = %s %q, %v, want %s %q", action, reason, err, provider.ActionKeep, provider.ReasonCurrent)
	}
}

func TestADatabaseMadeSmallerThanItsTierPlansAnUpdateNamingWhatResizes(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	settings := h.server.Instances[h.server.Created[0].Name].Settings
	settings.Tier, settings.DeletionProtectionEnabled = "db-f1-micro", false
	settings.BackupConfiguration = &sqladmin.BackupConfiguration{Enabled: true}

	action, reason, err := h.plan(t, "17")
	if err != nil || action != provider.ActionUpdate {
		t.Fatalf("plan() = %s, %v, want %s: the provision that follows resizes and restarts the instance", action, err, provider.ActionUpdate)
	}
	for _, want := range []string{"machine type changes from db-f1-micro to db-custom-1-3840", "deletion protection changes from false to true", "point-in-time recovery changes from false to true"} {
		if !strings.Contains(reason, want) {
			t.Errorf("plan() reason = %q, want it to say %q", reason, want)
		}
	}
}

func TestADatabaseWhosePasswordWasLostPlansAnUpdate(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	if err := records.Forget(context.Background(), h.records, h.instances.passwordKey("orders")); err != nil {
		t.Fatal(err)
	}

	action, reason, err := h.plan(t, "17")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "password") {
		t.Errorf("plan() = %s %q, %v, want an update saying the ocel user is given a new password", action, reason, err)
	}
}

func TestADatabaseMissingItsOcelDatabasePlansAnUpdate(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	delete(h.server.Databases, h.server.Created[0].Name)

	action, reason, err := h.plan(t, "17")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "ocel database is created") {
		t.Errorf("plan() = %s %q, %v, want an update saying the ocel database is created", action, reason, err)
	}
}

func TestADatabaseAskingForAnotherMajorVersionThanItRunsIsRefusedAtPlan(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "16")

	_, _, err := h.plan(t, "17")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("plan() of version 17 over an instance running 16 = %v, want the %s refusal the provision would give", err, refusal.CodeInvalid)
	}
}
