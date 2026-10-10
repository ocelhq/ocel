package gcp

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
)

const (
	stateBucketSuffix     = "-state"
	passphraseSuffix      = "-pulumi-passphrase"
	edgeCredentialsSuffix = "-edge-credentials"
	isrWriterSeedSuffix   = "-isr-writer-seed"
)

const (
	accountDomain      = ".iam.gserviceaccount.com"
	dockerRegistryHost = "-docker.pkg.dev"
)

const (
	maxBucketName        = 63
	maxAccountID         = 30
	minDatabaseID        = 4
	maxServiceNameLength = 49
	maxTaggedLabelLength = 46
	revisionTagLength    = 9

	maxAppServiceNameLength = maxTaggedLabelLength - revisionTagLength
	serviceHashLength       = 6
	accountHashLen          = 10
)

var uuidLike = regexp.MustCompile(`[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}`)

type Names struct {
	namespace provider.Namespace
	project   string
}

func (n Names) Namespace() provider.Namespace { return n.namespace }

func (n Names) Project() string { return n.project }

func (n Names) Bucket(tier environment.Tier) string {
	return string(n.namespace) + "-" + n.project + "-" + string(tier)
}

func (n Names) StateBucket(tier environment.Tier) string {
	return n.Bucket(tier) + stateBucketSuffix
}

func (n Names) Repository(tier environment.Tier) string { return n.Bucket(tier) }

func (n Names) RepositoryPath(region string, tier environment.Tier) string {
	return region + dockerRegistryHost + "/" + n.project + "/" + n.Repository(tier)
}

const (
	appAccountInfix   = "app"
	appAccountsSuffix = "app_accounts"
	cdnPurgeSuffix    = "cdn_purge"
)

func customRoleID(ns provider.Namespace, suffix string) string {
	return strings.ReplaceAll(string(ns), "-", "_") + "_" + suffix
}

func (n Names) AppAccountsRole() string { return customRoleID(n.namespace, appAccountsSuffix) }

func (n Names) AppAccountsRolePath() string {
	return "projects/" + n.project + "/roles/" + n.AppAccountsRole()
}

func (n Names) CDNPurgeRole() string { return customRoleID(n.namespace, cdnPurgeSuffix) }

func (n Names) CDNPurgeRolePath() string {
	return "projects/" + n.project + "/roles/" + n.CDNPurgeRole()
}

func (n Names) AppAccount(tier environment.Tier, project, app string) string {
	return string(n.namespace) + "-" + truncatedHash(accountHashLen, string(tier), project, app, appAccountInfix)
}

func (n Names) AppAccountEmail(tier environment.Tier, project, app string) string {
	return n.AppAccount(tier, project, app) + "@" + n.project + accountDomain
}

func (n Names) Service(project, env, app, function string) (string, error) {
	parts := []string{string(n.namespace), naming.Sanitize(project), naming.Sanitize(env), naming.Sanitize(app)}
	if function != app {
		parts = append(parts, images.FunctionRoute(app, function))
	}
	room := maxAppServiceNameLength - serviceHashLength - 1
	if len(n.namespace) >= room {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the Cloud Run service of %s would start with the namespace %s, and a service a release tags has %d characters before its %d-character hash, "+
				"so Cloud Run can put the %d-character revision tag in front of it in a url: name a shorter namespace in %s",
			app, n.namespace, room, serviceHashLength, revisionTagLength, provider.NamespaceEnvVar)
	}
	return fitReadable(parts, room) + "-" + serviceHash(string(n.namespace), project, env, app, function), nil
}

func fitReadable(parts []string, room int) string {
	for _, cut := range []int{1, 2} {
		over := len(strings.Join(parts, "-")) - room
		if over <= 0 {
			break
		}
		parts[cut] = strings.Trim(parts[cut][:max(len(parts[cut])-over, 1)], "-")
	}
	readable := strings.Join(parts, "-")
	return strings.TrimRight(readable[:min(len(readable), room)], "-")
}

const workerInfix = "w"

func (n Names) WorkerService(project, env, app, worker string) string {
	readable := strings.Join([]string{string(n.namespace), naming.Sanitize(project), naming.Sanitize(env), workerInfix, naming.Sanitize(worker)}, "-")
	readable = strings.TrimRight(readable[:min(len(readable), maxServiceNameLength-serviceHashLength-1)], "-")
	return readable + "-" + serviceHash(string(n.namespace), project, env, app, workerInfix, worker)
}

const (
	realtimeInfix        = "realtime"
	realtimeKeysInfix    = "keys"
	realtimeSigningInfix = "signing"
	realtimeSecretSep    = "_"
	maxReadableSecretID  = 200
)

func (n Names) RealtimeGateway(project, env string) string {
	readable := strings.Join([]string{string(n.namespace), naming.Sanitize(project), naming.Sanitize(env), realtimeInfix}, "-")
	readable = strings.TrimRight(readable[:min(len(readable), maxServiceNameLength-serviceHashLength-1)], "-")
	return readable + "-" + serviceHash(string(n.namespace), project, env, realtimeInfix)
}

func (n Names) RealtimeSecretPrefix() string {
	return string(n.namespace) + realtimeSecretSep + realtimeInfix + "-"
}

func (n Names) RealtimeKeysSecretPrefix(tier environment.Tier) string {
	return n.RealtimeSecretPrefix() + realtimeKeysInfix + "-" + string(tier) + "-"
}

func (n Names) RealtimeKeysSecret(tier environment.Tier, project, env string) string {
	return realtimeSecret(n.RealtimeKeysSecretPrefix(tier), []string{project, env},
		string(n.namespace), string(tier), project, env, realtimeInfix, realtimeKeysInfix)
}

func (n Names) RealtimeSigningSecret(project, env, resource string) string {
	return realtimeSecret(n.RealtimeSecretPrefix()+realtimeSigningInfix+"-", []string{project, env, resource},
		string(n.namespace), project, env, realtimeInfix, realtimeSigningInfix, resource)
}

func realtimeSecret(prefix string, readableParts []string, hashed ...string) string {
	sanitized := make([]string, 0, len(readableParts))
	for _, part := range readableParts {
		sanitized = append(sanitized, naming.Sanitize(part))
	}
	readable := prefix + strings.Join(sanitized, "-")
	readable = strings.TrimRight(readable[:min(len(readable), maxReadableSecretID)], "-")
	return readable + "-" + serviceHash(hashed...)
}

func (n Names) RealtimeAccount(tier environment.Tier) string {
	return string(n.namespace) + "-" + truncatedHash(accountHashLen, string(tier), realtimeInfix)
}

func (n Names) RealtimeAccountEmail(tier environment.Tier) string {
	return n.RealtimeAccount(tier) + "@" + n.project + accountDomain
}

const assetReaderInfix = "assets"

func (n Names) AssetReaderAccount(tier environment.Tier) string {
	return string(n.namespace) + "-" + truncatedHash(accountHashLen, string(tier), assetReaderInfix)
}

func (n Names) AssetReaderAccountEmail(tier environment.Tier) string {
	return n.AssetReaderAccount(tier) + "@" + n.project + accountDomain
}

var cloudRunService = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

func serviceHash(parts ...string) string { return truncatedHash(serviceHashLength, parts...) }

func truncatedHash(length int, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:length]
}

const (
	maxInstanceNameLength = 63
	instanceHashLength    = 6
	memorystorePolicy     = "memorystore"
	cloudSQLPolicy        = "cloudsql"
)

func (n Names) KVInstance(project, env, store string) (string, error) {
	parts := []string{string(n.namespace), naming.Sanitize(project), naming.Sanitize(env), naming.Sanitize(store)}
	instance := strings.Join(parts, "-") + "-" + truncatedHash(instanceHashLength, string(n.namespace), project, env, store)
	if len(instance) > maxInstanceNameLength {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"kv %s would be the Memorystore instance %s, which is %d characters and Memorystore takes %d: "+
				"an instance is named for the namespace, the project, the environment and the store, "+
				"and ends in %d characters of a hash of the four.\n"+
				"Name a shorter namespace in %s, a shorter project slug, or a shorter store",
			store, instance, len(instance), maxInstanceNameLength, instanceHashLength, provider.NamespaceEnvVar)
	}
	return instance, nil
}

const (
	appBucketHashLength = 8
	namespaceEnd        = "--"
)

func (n Names) AppBucketPrefix() string { return string(n.namespace) + namespaceEnd }

func (n Names) AppBucket(project, env, bucket string) string {
	readable := n.AppBucketPrefix() + strings.Join([]string{naming.Sanitize(project), naming.Sanitize(env), naming.Sanitize(bucket)}, "-")
	readable = strings.TrimRight(readable[:min(len(readable), maxBucketName-appBucketHashLength-1)], "-")
	return readable + "-" + truncatedHash(appBucketHashLength, n.project, string(n.namespace), project, env, bucket)
}

const instanceSuffixBytes = 3

func (n Names) PostgresInstancePrefix() string { return string(n.namespace) + namespaceEnd }

func (n Names) PostgresInstance(project, env, database string) string {
	readable := n.PostgresInstancePrefix() + strings.Join([]string{naming.Sanitize(project), naming.Sanitize(env), naming.Sanitize(database)}, "-")
	return strings.TrimRight(readable[:min(len(readable), maxInstanceNameLength-2*instanceSuffixBytes-1)], "-")
}

func (n Names) Network(tier environment.Tier) string {
	return string(n.namespace) + "-" + string(tier)
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

const envSourceSyncName = "envsourcesync"

func (n Names) EnvSourceSync(tier environment.Tier) string {
	return string(n.namespace) + "-" + string(tier) + "-" + envSourceSyncName
}

func (n Names) EnvSourceSyncAccount(tier environment.Tier) string {
	return string(n.namespace) + "-" + truncatedHash(accountHashLen, string(tier), envSourceSyncName)
}

func (n Names) EnvSourceSyncAccountEmail(tier environment.Tier) string {
	return n.EnvSourceSyncAccount(tier) + "@" + n.project + accountDomain
}

const bastionName = "bastion"

func (n Names) Bastion(tier environment.Tier) string {
	return string(n.namespace) + "-" + string(tier) + "-" + bastionName
}

func (n Names) BastionAccount(tier environment.Tier) string {
	return string(n.namespace) + "-" + truncatedHash(accountHashLen, string(tier), bastionName)
}

func (n Names) BastionAccountEmail(tier environment.Tier) string {
	return n.BastionAccount(tier) + "@" + n.project + accountDomain
}

const connectorSuffix = "-connector"

func (n Names) Connector() string { return string(n.namespace) + connectorSuffix }

func (n Names) ConnectorAccountEmail() string {
	return n.Connector() + "@" + n.project + accountDomain
}

func (n Names) ConnectorKeySecret() string { return n.Connector() + "-key" }

func (n Names) connectorFits() error {
	if length := len(n.Connector()); length > maxAccountID {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %q service account the connector runs as is %d characters and IAM takes %d.\n"+
				"Name a shorter namespace in %s",
			n.Connector(), length, maxAccountID, provider.NamespaceEnvVar)
	}
	return nil
}

func (n Names) Database() string { return ports.Database(n.namespace) }

func (n Names) KeyRing() string { return ports.KeyRing(n.namespace) }

const (
	delayQueueSuffix = "-delays"
	pushAccountName  = "push"

	refreshAccountName = "refresh"
)

func (n Names) TaskDatabase(tier environment.Tier) string {
	return ports.TaskDatabase(n.namespace, tier)
}

func (n Names) TagDatabase(tier environment.Tier) string {
	return ports.TagDatabase(n.namespace, tier)
}

func (n Names) DelayQueue(tier environment.Tier) string {
	return string(n.namespace) + "-" + string(tier) + delayQueueSuffix
}

func (n Names) DelayQueuePath(region string, tier environment.Tier) string {
	return "projects/" + n.project + "/locations/" + region + "/queues/" + n.DelayQueue(tier)
}

func (n Names) PushAccount(tier environment.Tier) string {
	return string(n.namespace) + "-" + truncatedHash(accountHashLen, string(tier), pushAccountName)
}

func (n Names) PushAccountEmail(tier environment.Tier) string {
	return n.PushAccount(tier) + "@" + n.project + accountDomain
}

func (n Names) RefreshAccount(tier environment.Tier) string {
	return string(n.namespace) + "-" + truncatedHash(accountHashLen, string(tier), refreshAccountName)
}

func (n Names) RefreshAccountEmail(tier environment.Tier) string {
	return n.RefreshAccount(tier) + "@" + n.project + accountDomain
}

func (n Names) isHashedAccountID(id string) bool {
	hash, found := strings.CutPrefix(id, string(n.namespace)+"-")
	return found && len(hash) == accountHashLen && strings.Trim(hash, "0123456789abcdef") == ""
}

func (n Names) PassphraseSecret(tier environment.Tier) string {
	return string(n.namespace) + "-" + string(tier) + passphraseSuffix
}

func (n Names) EdgeCredentialsSecret(tier environment.Tier, kind edge.Kind) string {
	return string(n.namespace) + "-" + string(tier) + "-" + string(kind) + edgeCredentialsSuffix
}

func (n Names) ISRWriterSeedSecret(tier environment.Tier, kind edge.Kind) string {
	return string(n.namespace) + "-" + string(tier) + "-" + string(kind) + isrWriterSeedSuffix
}

func (n Names) fit() error {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, bucket := range []string{n.StateBucket(tier), n.Bucket(tier)} {
			if len(bucket) > maxBucketName {
				return refusal.Refuse(refusal.CodeInvalid,
					"the %s bucket this bootstrap names is %d characters and Cloud Storage takes %d: "+
						"namespace %q and project %q together leave nothing to cut.\n"+
						"Name a shorter namespace in %s, or deploy into a project with a shorter id",
					bucket, len(bucket), maxBucketName, n.namespace, n.project, provider.NamespaceEnvVar)
			}
		}
	}
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
	if length := len(n.namespace) + len("-") + accountHashLen; length > maxAccountID {
		return refusal.Refuse(refusal.CodeInvalid,
			"the service accounts this bootstrap names are %d characters and Google takes %d: "+
				"every tier and app account is the namespace, a dash and %d characters.\n"+
				"Name a shorter namespace in %s",
			length, maxAccountID, accountHashLen, provider.NamespaceEnvVar)
	}
	return nil
}
