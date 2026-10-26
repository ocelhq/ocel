package vps

import (
	"context"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/topic"
)

var ProviderOver = newProvider

var NewFrontedEnv = newFrontedEnv

type KeptDelivery struct {
	RouteTableKey, BundleKey, Envelope string
}

func KeepReleaseObjects(ctx context.Context, endpoint, credential string, spec provider.StackSpec) (KeptDelivery, error) {
	held := fronting{
		store: edge.ReleasesStore{Endpoint: endpoint, BootstrapCredential: credential},
		kept:  cloudflare.NewKeptObjects("ocel", cloudflare.Origin{}),
	}
	delivered, err := keepReleaseObjects(ctx, held, spec, progress.Discard())
	return KeptDelivery{RouteTableKey: delivered.routeTableKey, BundleKey: delivered.bundleKey, Envelope: delivered.envelope}, err
}

const (
	RealtimeGatewayMemoryBytes = realtime.GatewayMemoryBytes
	RealtimeSocketBytes        = realtime.SocketBytes
)

func (p *Provider) FindTunnelHooks(kind edge.Kind) (*edge.TunnelHooks, error) {
	return p.findTunnelHooks(kind)
}

func (p *Provider) Timing(now func() time.Time) { p.now = now }

func (p *Provider) Clock() func() time.Time { return p.now }

var MintStoreSecret = bucket.MintSecret

var NewStoreSecretAssociatedData = bucket.NewSecretAssociatedData

var StoreAccountKey = bucket.AccountKey

var CheckStoreSecret = bucket.CheckSecret

var NewPostgresSecretAssociatedData = postgres.NewSecretAssociatedData

func (p *Provider) PlanPostgres(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	return postgres.New(p.components()).Plan(ctx, in, recorded)
}

func (p *Provider) ProvisionPostgres(ctx context.Context, in resources.ProvisionRequest, log progress.Log) (provider.Binding, error) {
	return postgres.New(p.components()).Provision(ctx, in, log)
}

func (p *Provider) RemovePostgres(ctx context.Context, ref provider.StackRef, binding provider.Binding, log progress.Log) error {
	return postgres.New(p.components()).Remove(ctx, ref, binding, log)
}

func (p *Provider) PromoteWorkers(ctx context.Context, tier environment.Tier, project, env, app, appContainer string, log progress.Log) error {
	return topic.NewWorkers(p.components()).Promote(ctx, tier, project, env, app, appContainer, log)
}

func (p *Provider) PlanKV(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	return kv.New(p.components()).Plan(ctx, in, recorded)
}

func (p *Provider) ProvisionKV(ctx context.Context, in resources.ProvisionRequest, log progress.Log) (provider.Binding, error) {
	return kv.New(p.components()).Provision(ctx, in, log)
}

func (p *Provider) RemoveKV(ctx context.Context, ref provider.StackRef, binding provider.Binding, log progress.Log) error {
	return kv.New(p.components()).Remove(ctx, ref, binding, log)
}

func (p *Provider) PlanTopic(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	return topic.New(p.components(), &p.queues).Plan(ctx, in, recorded)
}

func (p *Provider) ProvisionTopic(ctx context.Context, in resources.ProvisionRequest, log progress.Log) (provider.Binding, error) {
	return topic.New(p.components(), &p.queues).Provision(ctx, in, log)
}

func (p *Provider) RemoveTopic(ctx context.Context, ref provider.StackRef, binding provider.Binding, log progress.Log) error {
	return topic.New(p.components(), &p.queues).Remove(ctx, ref, binding, log)
}

func (p *Provider) PlanRealtime(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	return realtime.New(p.components()).Plan(ctx, in, recorded)
}

func (p *Provider) ProvisionRealtime(ctx context.Context, in resources.ProvisionRequest, log progress.Log) (provider.Binding, error) {
	return realtime.New(p.components()).Provision(ctx, in, log)
}

func (p *Provider) RemoveRealtime(ctx context.Context, ref provider.StackRef, binding provider.Binding, log progress.Log) error {
	return realtime.New(p.components()).Remove(ctx, ref, binding, log)
}

func (p *Provider) PlanBucket(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	return p.buckets().Plan(ctx, in, recorded)
}

func (p *Provider) ProvisionBucket(ctx context.Context, in resources.ProvisionRequest, log progress.Log) (provider.Binding, error) {
	return p.buckets().Provision(ctx, in, log)
}

func (p *Provider) RemoveBucket(ctx context.Context, ref provider.StackRef, binding provider.Binding, log progress.Log) error {
	return p.buckets().Remove(ctx, ref, binding, log)
}

func StoreName(t testing.TB, ref provider.StackRef) string {
	t.Helper()
	name, err := bucket.StoreName(ref)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func Whoami(ctx context.Context, live hostSurvey) (provider.Principal, error) {
	return whoami(ctx, live)
}

func Exposed(inner provider.Bootstrap, gate func(context.Context) error, refuseClosedPorts func(context.Context, environment.Tier) error) provider.Bootstrap {
	return exposed{elevating: elevating{Bootstrap: inner, elevated: gate}, refuseClosedPorts: refuseClosedPorts}
}

func Fronted(inner provider.Bootstrap, gate func(context.Context) error, front edge.Edge) provider.Bootstrap {
	return fronted{elevating: elevating{Bootstrap: inner, elevated: gate}, front: front, forget: func(context.Context, environment.Tier) error { return nil }}
}

func (p *Provider) Host() *host.Host { return p.host }

func (p *Provider) Recording(store records.Store) { p.keyValues = store }

func (p *Provider) Sealing(cipher seal.Cipher) { p.cipher = cipher }

func (p *Provider) RecordFronting(ctx context.Context, tier environment.Tier, credential, seed, store, endpoint string, projects ...string) error {
	return records.Change(ctx, p.keyValues, frontingRecordKey(tier), func(records.Entry) ([]byte, bool, error) {
		sealed, err := p.sealFrontingRecord(ctx, tier, frontingRecord{Credential: credential, PurgeSeed: seed, Store: store, Endpoint: endpoint, StoreVersion: cloudflare.ComputeStoreVersion(), Projects: projects})
		return sealed, true, err
	})
}

func (p *Provider) RecordFrontingStoreVersion(ctx context.Context, tier environment.Tier, version string) error {
	return records.Change(ctx, p.keyValues, frontingRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		record, err := p.openFrontingRecord(ctx, tier, entry.Value)
		if err != nil {
			return nil, false, err
		}
		record.StoreVersion = version
		sealed, err := p.sealFrontingRecord(ctx, tier, record)
		return sealed, true, err
	})
}

func (p *Provider) ReadFrontingStoreVersion(ctx context.Context, tier environment.Tier) (string, error) {
	record, _, err := p.readFronting(ctx, tier)
	return record.StoreVersion, err
}

func (p *Provider) InstallingStores(install func(ctx context.Context, tier environment.Tier, credential string) (edge.ReleasesStore, error)) {
	p.installStore = install
}

var ForgetFrontingStoreVersion = (*Provider).forgetFrontingStoreVersion

func (p *Provider) TearingDownStores(teardown func(ctx context.Context, tier environment.Tier) error) {
	p.tearDownStore = teardown
}

func (p *Provider) Deploying(project string) { p.project = project }

func FrontingRecordKey(tier environment.Tier) records.Key { return frontingRecordKey(tier) }

func (p *Provider) RecordFrontingRetiring(ctx context.Context, tier environment.Tier, since time.Time) error {
	return records.Change(ctx, p.keyValues, frontingRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		record, err := p.openFrontingRecord(ctx, tier, entry.Value)
		if err != nil {
			return nil, false, err
		}
		record.Projects, record.Retiring = nil, since
		sealed, err := p.sealFrontingRecord(ctx, tier, record)
		return sealed, true, err
	})
}

func (p *Provider) IsFrontingRecorded(ctx context.Context, tier environment.Tier) (bool, error) {
	_, held, err := p.readFronting(ctx, tier)
	return held, err
}

func (p *Provider) ReadFrontedProjects(ctx context.Context, tier environment.Tier) ([]string, error) {
	record, _, err := p.readFronting(ctx, tier)
	return record.Projects, err
}

var ForgetFrontedProject = (*Provider).forgetFrontedProject

func (p *Provider) Resolving(look Lookup) { p.resolve = look }

func (p *Provider) Reaching(dial Reach) { p.reaches = dial }

func (p *Provider) Transforming(pass transform.Pass) { p.transform = pass }

func DNSVerdict(ctx context.Context, look Lookup, hostname, address string) provider.HostCheck {
	here, unread := look(ctx, address)
	return dnsVerdict(ctx, look, hostname, address, here, unread)
}

func ProxiedVerdict(ctx context.Context, look Lookup, serving provider.Liveness, hostname, address string) provider.HostCheck {
	return proxiedVerdict(ctx, look, serving, hostname, address)
}

func DNSVerdicts(ctx context.Context, look Lookup, hostnames []string, address string) []provider.HostCheck {
	return dnsVerdicts(ctx, look, hostnames, address)
}

func ReachVerdict(ctx context.Context, dial Reach, address string) provider.HostCheck {
	return reachVerdict(ctx, dial, address)
}

func (p *Provider) Fronted() *front.Proxy { return p.options.Proxy }

func (p *Provider) RefuseServingPortsClosedFromOutside(ctx context.Context, tier environment.Tier) error {
	return p.refuseServingPortsClosedFromOutside(ctx, tier)
}

var NewRecordedGate = newRecordedGate

func NewReadingSSHConfig(config string) func(context.Context, provider.Settings) (provider.Provider, error) {
	return func(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
		served, err := New(ctx, settings)
		if err != nil {
			return nil, err
		}
		served.(*Provider).options.SSH.Config = config
		return served, nil
	}
}

func (p *Provider) PullingImagesWith(pull images.ImagePull) *Provider {
	p.pullImage = pull
	return p
}
