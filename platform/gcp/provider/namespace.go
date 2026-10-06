package gcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	stateBucketSuffix = "-state"
	passphraseSuffix  = "-pulumi-passphrase"
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
	serviceHashLength    = 6
	accountHashLen       = 10
)

const longestTier = environment.TierProduction

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

func (n Names) DelayAccount(tier environment.Tier) string {
	return string(n.namespace) + "-" + string(tier)
}

func (n Names) DelayAccountEmail(tier environment.Tier) string {
	return n.DelayAccount(tier) + "@" + n.project + accountDomain
}

const appAccountInfix = "app"

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
	service := strings.Join(parts, "-") + "-" + serviceHash(string(n.namespace), project, env, app, function)
	if len(service) > maxServiceNameLength {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the Cloud Run service %s would be named %s, which is %d characters and Cloud Run builds a url from %d: "+
				"a service is named for the namespace, the project, the environment and the app it serves, "+
				"and ends in %d characters of a hash of the four, because every one of them may contain a dash and a name joined by dashes alone would read two ways.\n"+
				"Name a shorter namespace in %s, a shorter project slug, or a shorter app",
			app, service, len(service), maxServiceNameLength, serviceHashLength, provider.NamespaceEnvVar)
	}
	return service, nil
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

const functionSuffix = "fn"

var cloudRunService = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

func (n Names) PreviewService(label, app, function string) (string, error) {
	service, suffix := label, ""
	if function != app {
		suffix = naming.FieldSeparator + images.FunctionRoute(app, function) + naming.FieldSeparator + functionSuffix
		service += suffix
	}
	if len(service) <= maxServiceNameLength && cloudRunService.MatchString(service) {
		return service, nil
	}
	slug := edge.PreviewHost{Hostname: label}.ReadPrefix()
	shape := fmt.Sprintf("the project slug %q (%d characters) and a %d-character token", slug, len(slug), len("-")+edge.PreviewTailLen)
	if suffix != "" {
		shape += fmt.Sprintf(", and a function's service adds %q", suffix)
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"the preview %s would be served by a Cloud Run service named %q (%d characters), which Cloud Run will not take: "+
			"Cloud Run takes at most %d characters of lowercase letters, digits and dashes, starting with a letter and ending with a letter or a digit. "+
			"On a shared preview wildcard the load balancer hands Cloud Run the label a hostname begins with as the service name, "+
			"and that label is %s.\n"+
			"Use a project slug that starts with a letter and leaves the name within %d characters, and deploy again",
		app, service, len(service), maxServiceNameLength, shape, maxServiceNameLength)
}

func serviceHash(parts ...string) string { return truncatedHash(serviceHashLength, parts...) }

func truncatedHash(length int, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:length]
}

const (
	maxInstanceNameLength = 63
	instanceHashLength    = 6
	memorystorePolicy     = "memorystore"
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

func (n Names) ConnectionPolicy(tier environment.Tier) string {
	return n.Network(tier) + "-" + memorystorePolicy
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

func (n Names) PassphraseSecret(tier environment.Tier) string {
	return string(n.namespace) + "-" + string(tier) + passphraseSuffix
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
	if account := n.DelayAccount(longestTier); len(account) > maxAccountID {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s service account this bootstrap names is %d characters and Google takes %d: "+
				"a delayed message of the %s tier is published as it, so the tier is part of its name.\n"+
				"Name a shorter namespace in %s",
			account, len(account), maxAccountID, longestTier, provider.NamespaceEnvVar)
	}
	return nil
}
