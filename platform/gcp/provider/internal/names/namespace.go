package names

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	minDatabaseID        = 4
	MaxServiceNameLength = 49
	maxTaggedLabelLength = 46
	revisionTagLength    = 9

	maxAppServiceNameLength = maxTaggedLabelLength - revisionTagLength
	serviceHashLength       = 6
	AccountHashLen          = 10
)

var uuidLike = regexp.MustCompile(`[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}`)

type Names struct {
	namespace provider.Namespace
	project   string
}

func (n Names) Namespace() provider.Namespace { return n.namespace }

func (n Names) Project() string { return n.project }

func (n Names) Repository(tier environment.Tier) string {
	return n.mustOcelName(naming.TierWord(tier), repositoryWord)
}

func (n Names) RepositoryPath(region string, tier environment.Tier) string {
	return region + DockerRegistryHost + "/" + n.project + "/" + n.Repository(tier)
}

const (
	appAccountsSuffix = "app_accounts"
	cdnPurgeSuffix    = "cdn_purge"
)

func customRoleID(ns provider.Namespace, suffix string) string {
	return strings.ReplaceAll(naming.NamespaceField(string(ns)), "-", "_") + "_" + suffix
}

func (n Names) AppAccountsRole() string { return customRoleID(n.namespace, appAccountsSuffix) }

func (n Names) AppAccountsRolePath() string {
	return "projects/" + n.project + "/roles/" + n.AppAccountsRole()
}

func (n Names) CDNPurgeRole() string { return customRoleID(n.namespace, cdnPurgeSuffix) }

func (n Names) CDNPurgeRolePath() string {
	return "projects/" + n.project + "/roles/" + n.CDNPurgeRole()
}

func (n Names) AppAccount(tier environment.Tier, project, env, app string) string {
	return n.AppFence() + truncatedHash(AccountHashLen, string(n.namespace), string(tier), project, env, app)
}

func (n Names) AppAccountEmail(tier environment.Tier, project, env, app string) string {
	return n.AppAccount(tier, project, env, app) + "@" + n.project + AccountDomain
}

func (n Names) Service(project, env, app, function string) (string, error) {
	tag := naming.Coordinate{Project: project, Env: env, App: app, Kind: naming.KindFunction, Name: function}.HashTag(string(n.namespace))
	name := naming.PhysicalName{Project: project, Env: env, Declared: app, Tag: tag}
	if function != app {
		name.App, name.Declared = app, images.FunctionRoute(app, function)
	}
	return n.fitApp(name, maxAppServiceNameLength, "app "+app+"'s function", "Cloud Run service a tagged revision's url is built from")
}

func (n Names) WorkerService(project, env, app, worker string) (string, error) {
	tag := naming.Coordinate{Project: project, Env: env, App: app, Kind: naming.KindWorker, Name: worker}.HashTag(string(n.namespace))
	return n.fitApp(naming.PhysicalName{Project: project, Env: env, App: app, Declared: worker, Tag: tag}, MaxServiceNameLength, "worker", "Cloud Run service")
}

const (
	realtimeInfix        = "realtime"
	realtimeKeysInfix    = "keys"
	realtimeSigningInfix = "signing"
	namespaceSecretSep   = "_"
	maxReadableSecretID  = 200
)

func (n Names) RealtimeGateway(project, env string) string {
	tag := naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: naming.KindRealtime, Name: realtimeInfix}.HashTag(string(n.namespace))
	gateway, err := naming.PhysicalName{Namespace: string(n.namespace), Project: project, Env: env, Declared: realtimeInfix, Tag: tag}.Fit(MaxServiceNameLength)
	if err != nil {
		panic(err)
	}
	return gateway
}

const gateInfix = "gate"

func (n Names) secretPrefix(infix string) string {
	return naming.NamespaceField(string(n.namespace)) + namespaceSecretSep + infix + naming.WordSeparator
}

func (n Names) GateSecretPrefix() string { return n.secretPrefix(gateInfix) }

func (n Names) GateTierSecretPrefix(tier environment.Tier) string {
	return n.GateSecretPrefix() + naming.TierWord(tier) + naming.WordSeparator
}

func (n Names) GateSecret(tier environment.Tier, slug string) string {
	return hashedSecretID(n.GateTierSecretPrefix(tier), []string{slug}, string(n.namespace), string(tier), gateInfix, slug)
}

func (n Names) RealtimeSecretPrefix() string { return n.secretPrefix(realtimeInfix) }

func (n Names) RealtimeKeysSecretPrefix(tier environment.Tier) string {
	return n.RealtimeSecretPrefix() + realtimeKeysInfix + naming.WordSeparator + naming.TierWord(tier) + naming.WordSeparator
}

func (n Names) RealtimeKeysSecret(tier environment.Tier, project, env string) string {
	return hashedSecretID(n.RealtimeKeysSecretPrefix(tier), []string{project, env},
		string(n.namespace), string(tier), project, env, realtimeInfix, realtimeKeysInfix)
}

func (n Names) RealtimeSigningSecret(project, env, resource string) string {
	return hashedSecretID(n.RealtimeSecretPrefix()+realtimeSigningInfix+naming.WordSeparator, []string{project, env, resource},
		string(n.namespace), project, env, realtimeInfix, realtimeSigningInfix, resource)
}

func hashedSecretID(prefix string, readableParts []string, hashed ...string) string {
	readable := prefix + naming.Join(naming.WordSeparator, readableParts...)
	readable = strings.TrimRight(readable[:min(len(readable), maxReadableSecretID)], naming.WordSeparator)
	return readable + naming.WordSeparator + naming.HashTag(hashed...)
}

func (n Names) tierAccount(tier environment.Tier, word string) string {
	return n.mustOcelName(naming.TierWord(tier), word)
}

func (n Names) RealtimeAccount(tier environment.Tier) string {
	return n.tierAccount(tier, realtimeInfix)
}

func (n Names) RealtimeAccountEmail(tier environment.Tier) string {
	return n.RealtimeAccount(tier) + "@" + n.project + AccountDomain
}

const assetReaderInfix = "assets"

func (n Names) AssetReaderAccount(tier environment.Tier) string {
	return n.tierAccount(tier, assetReaderInfix)
}

func (n Names) AssetReaderAccountEmail(tier environment.Tier) string {
	return n.AssetReaderAccount(tier) + "@" + n.project + AccountDomain
}

var CloudRunService = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

func truncatedHash(length int, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:length]
}

const (
	maxInstanceNameLength = 63
	MemorystorePolicy     = "memorystore"
	CloudSQLPolicy        = "cloudsql"
)

func (n Names) KVInstance(project, env, store string) (string, error) {
	tag := naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: naming.KindKV, Name: store}.HashTag(string(n.namespace))
	return n.fitApp(naming.PhysicalName{Project: project, Env: env, Declared: store, Tag: tag}, maxInstanceNameLength, "kv", "Memorystore instance")
}

func (n Names) AppFence() string { return naming.AppFence(string(n.namespace)) }

func (n Names) AppBucket(project, env, bucket, tag string) (string, error) {
	return n.fitApp(naming.PhysicalName{Project: project, Env: env, Declared: bucket, Tag: tag}, maxBucketName, "bucket", "Cloud Storage bucket")
}

func (n Names) UntaggedAppBucket(project, env, bucket string) (string, error) {
	return untagged(func(tag string) (string, error) { return n.AppBucket(project, env, bucket, tag) })
}

func (n Names) PostgresInstance(project, env, database, tag string) (string, error) {
	return n.fitApp(naming.PhysicalName{Project: project, Env: env, Declared: database, Tag: tag}, maxInstanceNameLength, "postgres", "Cloud SQL instance")
}

func (n Names) UntaggedPostgresInstance(project, env, database string) (string, error) {
	return untagged(func(tag string) (string, error) { return n.PostgresInstance(project, env, database, tag) })
}

func untagged(named func(tag string) (string, error)) (string, error) {
	placeholder := naming.HashTag()
	name, err := named(placeholder)
	return strings.TrimSuffix(name, naming.WordSeparator+placeholder), err
}

func (n Names) fitApp(name naming.PhysicalName, limit int, primitive, service string) (string, error) {
	name.Namespace = string(n.namespace)
	fitted, err := name.Fit(limit)
	if err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s %s would be a %s, and %s takes at most %d characters: %v.\n"+
				"Name a shorter %s, or a shorter namespace in %s",
			primitive, name.Declared, service, service, limit, err, primitive, provider.NamespaceEnvVar)
	}
	return fitted, nil
}

func (n Names) Network(tier environment.Tier) string {
	return n.mustOcelName(naming.TierWord(tier), networkWord)
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
	return n.mustOcelName(naming.TierWord(tier), envSourceSyncName)
}

func (n Names) EnvSourceSyncAccount(tier environment.Tier) string {
	return n.tierAccount(tier, envSourceSyncName)
}

func (n Names) EnvSourceSyncAccountEmail(tier environment.Tier) string {
	return n.EnvSourceSyncAccount(tier) + "@" + n.project + AccountDomain
}

const bastionName = "bastion"

func (n Names) Bastion(tier environment.Tier) string {
	return n.mustOcelName(naming.TierWord(tier), bastionName)
}

func (n Names) BastionAccount(tier environment.Tier) string {
	return n.tierAccount(tier, bastionName)
}

func (n Names) BastionAccountEmail(tier environment.Tier) string {
	return n.BastionAccount(tier) + "@" + n.project + AccountDomain
}

const connectorWord = "connector"

func (n Names) Connector() string { return n.mustOcelName(connectorWord) }

func (n Names) ConnectorAccountEmail() string {
	return n.Connector() + "@" + n.project + AccountDomain
}

func (n Names) ConnectorKeySecret() string { return n.Connector() + "-key" }

func (n Names) Database() string { return ports.Database(n.namespace) }

func (n Names) KeyRing() string { return ports.KeyRing(n.namespace) }

const (
	delayQueueWord  = "delays"
	pushAccountName = "push"
)

func (n Names) DelayQueue(tier environment.Tier) string {
	return n.mustOcelName(naming.TierWord(tier), delayQueueWord)
}

func (n Names) DelayQueuePath(region string, tier environment.Tier) string {
	return "projects/" + n.project + "/locations/" + region + "/queues/" + n.DelayQueue(tier)
}

func (n Names) PushAccount(tier environment.Tier) string {
	return n.tierAccount(tier, pushAccountName)
}

func (n Names) PushAccountEmail(tier environment.Tier) string {
	return n.PushAccount(tier) + "@" + n.project + AccountDomain
}

func (n Names) PassphraseSecret(tier environment.Tier) string {
	return n.mustOcelName(naming.TierWord(tier), passphraseWords)
}

func (n Names) EdgeCredentialsSecret(tier environment.Tier, kind edge.Kind) string {
	return n.mustOcelName(naming.TierWord(tier), string(kind), edgeCredentialsWord)
}

func (n Names) PurgeSeedSecret(tier environment.Tier, kind edge.Kind) string {
	return n.mustOcelName(naming.TierWord(tier), string(kind), purgeSeedWord)
}

func (n Names) Fit() error {
	if len(n.Database()) < minDatabaseID {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %q Firestore database this bootstrap names is %d characters and Firestore takes at least %d.\n"+
				"Name a longer namespace in %s",
			n.Database(), len(n.Database()), minDatabaseID, provider.NamespaceEnvVar)
	}
	if uuidLike.MatchString(n.Database()) {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %q Firestore database this bootstrap names reads as a UUID, and a Firestore database id may not.\n"+
				"Name a namespace in %s that does not",
			n.Database(), provider.NamespaceEnvVar)
	}
	return nil
}

const (
	partsSuffix                 = "parts"
	projectRecordsSuffix        = "project_records"
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

func (n Names) ProjectRecordsRole() string { return customRoleID(n.namespace, projectRecordsSuffix) }

func (n Names) ProjectRecordsRolePath() string {
	return "projects/" + n.project + "/roles/" + n.ProjectRecordsRole()
}

func (n Names) AccountID(account *iam.ServiceAccount) (string, bool) {
	return strings.CutSuffix(account.Email, "@"+n.project+AccountDomain)
}

func New(namespace provider.Namespace, project string) Names {
	return Names{namespace: namespace, project: project}
}

func (n Names) mustOcelName(words ...string) string {
	name, err := naming.OcelName(string(n.namespace), words...)
	if err != nil {
		panic(err)
	}
	return name
}
