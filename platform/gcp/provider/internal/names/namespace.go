package names

import (
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"google.golang.org/api/iam/v1"
)

const (
	repositoryWord      = "containers"
	networkWord         = "network"
	passphraseWords     = "pulumi-passphrase"
	edgeCredentialsWord = "edge-credentials"
	purgeSeedWord       = "purge-seed"
)

const (
	AccountDomain      = ".iam.gserviceaccount.com"
	DockerRegistryHost = "-docker.pkg.dev"
)

const (
	maxBucketName        = 63
	MaxAccountID         = 30
	MaxServiceNameLength = 49
	maxTaggedLabelLength = 46
	revisionTagLength    = 9

	maxAppServiceNameLength = maxTaggedLabelLength - revisionTagLength
	serviceHashLength       = 6
	AccountHashDigits       = 10
)

type Names struct {
	namespace provider.Namespace
	project   string
}

func (n Names) Namespace() provider.Namespace { return n.namespace }

func (n Names) Project() string { return n.project }

func (n Names) Repository(tier environment.Tier) string {
	return n.ocelName(tier, repositoryWord)
}

func (n Names) RepositoryPath(region string, tier environment.Tier) string {
	return region + DockerRegistryHost + "/" + n.project + "/" + n.Repository(tier)
}

const (
	cdnPurgeSuffix = "cdn_purge"
)

func customRoleID(ns provider.Namespace, suffix string) string {
	return strings.ReplaceAll(naming.NamespaceField(string(ns)), "-", "_") + "_" + suffix
}

func (n Names) CDNPurgeRole() string { return customRoleID(n.namespace, cdnPurgeSuffix) }

func (n Names) CDNPurgeRolePath() string {
	return "projects/" + n.project + "/roles/" + n.CDNPurgeRole()
}

func (n Names) AppAccount(project, env, app string) string {
	return n.AppFence() + naming.HashDigits(AccountHashDigits, string(n.namespace), project, env, app)
}

func (n Names) AppAccountEmail(project, env, app string) string {
	return n.AppAccount(project, env, app) + "@" + n.project + AccountDomain
}

func (n Names) Service(project, env, app, function string) (string, error) {
	tag := naming.Coordinate{Project: project, Env: env, App: app, Kind: naming.KindFunction, Name: function}.HashTag(string(n.namespace))
	name := naming.PhysicalName{Fence: n.AppFence(), Project: project, Env: env, App: app, Tag: tag}
	if function != app {
		name.Declared = images.FunctionRoute(app, function)
	}
	return name.Fit(maxAppServiceNameLength)
}

func (n Names) WorkerService(project, env, app, worker string) (string, error) {
	tag := naming.Coordinate{Project: project, Env: env, App: app, Kind: naming.KindWorker, Name: worker}.HashTag(string(n.namespace))
	return naming.PhysicalName{Fence: n.AppFence(), Project: project, Env: env, App: app, Declared: worker, Tag: tag}.Fit(MaxServiceNameLength)
}

const (
	realtimeInfix        = "realtime"
	realtimeKeysInfix    = "keys"
	realtimeSigningInfix = "signing"
	namespaceSecretSep   = "_"
	maxSecretID          = 255
)

func (n Names) RealtimeGateway(project, env string) (string, error) {
	return n.hashTagged(envCoordinate(project, env, naming.KindRealtime, realtimeInfix)).Fit(MaxServiceNameLength)
}

func envCoordinate(project, env string, kind naming.Kind, declared string) naming.Coordinate {
	return naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: kind, Name: declared}
}

func (n Names) hashTagged(c naming.Coordinate) naming.PhysicalName {
	return c.AppResourceName(string(n.namespace), c.HashTag(string(n.namespace)))
}

const gateInfix = "gate"

func (n Names) secretPrefix(infix string) string {
	return naming.NamespaceField(string(n.namespace)) + namespaceSecretSep + infix + naming.WordSeparator
}

func (n Names) GateSecretPrefix() string { return n.secretPrefix(gateInfix) }

func (n Names) GateTierSecretPrefix(tier environment.Tier) string {
	return n.GateSecretPrefix() + naming.TierWord(tier) + naming.WordSeparator
}

func (n Names) GateSecret(tier environment.Tier, slug string) (string, error) {
	return hashedSecretID(n.GateTierSecretPrefix(tier), []string{slug}, string(n.namespace), string(tier), gateInfix, slug)
}

func (n Names) RealtimeSecretPrefix() string { return n.secretPrefix(realtimeInfix) }

func (n Names) RealtimeKeysSecretPrefix(tier environment.Tier) string {
	return n.RealtimeSecretPrefix() + realtimeKeysInfix + naming.WordSeparator + naming.TierWord(tier) + naming.WordSeparator
}

func (n Names) RealtimeKeysSecret(tier environment.Tier, project, env string) (string, error) {
	return hashedSecretID(n.RealtimeKeysSecretPrefix(tier), []string{project, env},
		string(n.namespace), string(tier), project, env, realtimeInfix, realtimeKeysInfix)
}

func (n Names) RealtimeSigningSecret(project, env, resource string) (string, error) {
	return hashedSecretID(n.RealtimeSecretPrefix()+realtimeSigningInfix+naming.WordSeparator, []string{project, env, resource},
		string(n.namespace), project, env, realtimeInfix, realtimeSigningInfix, resource)
}

func hashedSecretID(prefix string, readableParts []string, hashed ...string) (string, error) {
	return naming.TaggedName{
		Head:     strings.TrimSuffix(prefix, naming.WordSeparator),
		Readable: naming.Join(naming.WordSeparator, readableParts...),
		Tag:      naming.HashTag(hashed...),
	}.Fit(maxSecretID)
}

func (n Names) RealtimeAccount(tier environment.Tier) string {
	return n.ocelName(tier, realtimeInfix)
}

func (n Names) RealtimeAccountEmail(tier environment.Tier) string {
	return n.RealtimeAccount(tier) + "@" + n.project + AccountDomain
}

const assetReaderInfix = "assets"

func (n Names) AssetReaderAccount(tier environment.Tier) string {
	return n.ocelName(tier, assetReaderInfix)
}

func (n Names) AssetReaderAccountEmail(tier environment.Tier) string {
	return n.AssetReaderAccount(tier) + "@" + n.project + AccountDomain
}

var CloudRunService = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

const (
	maxInstanceNameLength = 63
	MemorystorePolicy     = "memorystore"
	CloudSQLPolicy        = "cloudsql"
)

func (n Names) KVInstance(project, env, store string) (string, error) {
	return n.hashTagged(envCoordinate(project, env, naming.KindKV, store)).Fit(maxInstanceNameLength)
}

func (n Names) AppFence() string { return naming.AppFence(string(n.namespace)) }

func (n Names) AppBucket(project, env, bucket, tag string) (string, error) {
	return envCoordinate(project, env, naming.KindBucket, bucket).AppResourceName(string(n.namespace), tag).Fit(maxBucketName)
}

func (n Names) UntaggedAppBucket(project, env, bucket string) (string, error) {
	return envCoordinate(project, env, naming.KindBucket, bucket).AppResourceName(string(n.namespace), "").FitUntagged(maxBucketName)
}

func (n Names) PostgresInstance(project, env, database, tag string) (string, error) {
	return envCoordinate(project, env, naming.KindDatabase, database).AppResourceName(string(n.namespace), tag).Fit(maxInstanceNameLength)
}

func (n Names) UntaggedPostgresInstance(project, env, database string) (string, error) {
	return envCoordinate(project, env, naming.KindDatabase, database).AppResourceName(string(n.namespace), "").FitUntagged(maxInstanceNameLength)
}

func (n Names) Network(tier environment.Tier) string {
	return n.ocelName(tier, networkWord)
}

func (n Names) NetworkPath(tier environment.Tier) string {
	return "projects/" + n.project + "/global/networks/" + n.Network(tier)
}

func (n Names) Subnetwork(tier environment.Tier) string { return n.Network(tier) }

func (n Names) SubnetworkPath(region string, tier environment.Tier) string {
	return "projects/" + n.project + "/regions/" + region + "/subnetworks/" + n.Subnetwork(tier)
}

func (n Names) ConnectionPolicy(tier environment.Tier, service string) string {
	return n.Network(tier) + "-" + service
}

const envSourceSyncName = "env-sync"

func (n Names) EnvSourceSync(tier environment.Tier) string {
	return n.ocelName(tier, envSourceSyncName)
}

func (n Names) EnvSourceSyncAccount(tier environment.Tier) string {
	return n.ocelName(tier, envSourceSyncName)
}

func (n Names) EnvSourceSyncAccountEmail(tier environment.Tier) string {
	return n.EnvSourceSyncAccount(tier) + "@" + n.project + AccountDomain
}

const bastionName = "bastion"

func (n Names) Bastion(tier environment.Tier) string {
	return n.ocelName(tier, bastionName)
}

func (n Names) BastionAccount(tier environment.Tier) string {
	return n.ocelName(tier, bastionName)
}

func (n Names) BastionAccountEmail(tier environment.Tier) string {
	return n.BastionAccount(tier) + "@" + n.project + AccountDomain
}

const connectorWord = "connector"

func (n Names) Connector() string {
	return naming.MustUntieredOcelName(string(n.namespace), connectorWord)
}

func (n Names) ConnectorAccountEmail() string {
	return n.Connector() + "@" + n.project + AccountDomain
}

func (n Names) ConnectorKeySecret() string { return n.Connector() + "-key" }

func (n Names) RecordsDatabase(tier environment.Tier) string {
	return ports.RecordsDatabase(n.namespace, tier)
}

func (n Names) KeyRing() string { return ports.KeyRing(n.namespace) }

const (
	delayQueueWord  = "delays"
	pushAccountName = "push"
)

func (n Names) DelayQueue(tier environment.Tier) string {
	return n.ocelName(tier, delayQueueWord)
}

func (n Names) DelayQueuePath(region string, tier environment.Tier) string {
	return "projects/" + n.project + "/locations/" + region + "/queues/" + n.DelayQueue(tier)
}

func (n Names) PushAccount(tier environment.Tier) string {
	return n.ocelName(tier, pushAccountName)
}

func (n Names) PushAccountEmail(tier environment.Tier) string {
	return n.PushAccount(tier) + "@" + n.project + AccountDomain
}

func (n Names) PassphraseSecret(tier environment.Tier) string {
	return n.ocelName(tier, passphraseWords)
}

func (n Names) EdgeCredentialsSecret(tier environment.Tier, kind edge.Kind) string {
	return n.ocelName(tier, string(kind), edgeCredentialsWord)
}

func (n Names) PurgeSeedSecret(tier environment.Tier, kind edge.Kind) string {
	return n.ocelName(tier, string(kind), purgeSeedWord)
}

const (
	partsSuffix                 = "parts"
	realtimeSubscriptionsSuffix = "realtime_subscriptions"
)

func (n Names) PartsRole() string { return customRoleID(n.namespace, partsSuffix) }

func (n Names) PartsRolePath() string {
	return "projects/" + n.project + "/roles/" + n.PartsRole()
}

func (n Names) ProjectDatabase(tier environment.Tier, project string) string {
	return ports.ProjectDatabase(n.namespace, tier, project)
}

func (n Names) ProjectDatabasePrefix(tier environment.Tier) string {
	return ports.ProjectDatabasePrefix(n.namespace, tier)
}

func (n Names) RealtimeSubscriptionsRole() string {
	return customRoleID(n.namespace, realtimeSubscriptionsSuffix)
}

func (n Names) RealtimeSubscriptionsRolePath() string {
	return "projects/" + n.project + "/roles/" + n.RealtimeSubscriptionsRole()
}

func (n Names) ProjectWritableDatabase(tier environment.Tier, project string) string {
	return ports.ProjectWritableDatabase(n.namespace, tier, project)
}

func (n Names) ProjectWritableDatabasePrefix(tier environment.Tier) string {
	return ports.ProjectWritableDatabasePrefix(n.namespace, tier)
}

func (n Names) AccountID(account *iam.ServiceAccount) (string, bool) {
	return strings.CutSuffix(account.Email, "@"+n.project+AccountDomain)
}

func New(namespace provider.Namespace, project string) Names {
	return Names{namespace: namespace, project: project}
}

func (n Names) ocelName(tier environment.Tier, words ...string) string {
	return naming.OcelName(string(n.namespace), tier, words...)
}
