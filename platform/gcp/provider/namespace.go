package gcp

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	maxBucketName  = 63
	maxAccountID   = 30
	minDatabaseID  = 4
	maxServiceName = 49
	serviceHashLen = 6
)

const longestClass = edge.ClassProduction

var uuidLike = regexp.MustCompile(`[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}`)

type Names struct {
	namespace provider.Namespace
	project   string
}

func (n Names) Namespace() provider.Namespace { return n.namespace }

func (n Names) Project() string { return n.project }

func (n Names) Bucket(class edge.Class) string {
	return string(n.namespace) + "-" + n.project + "-" + string(class)
}

func (n Names) StateBucket(class edge.Class) string {
	return n.Bucket(class) + stateBucketSuffix
}

func (n Names) Repository(class edge.Class) string { return n.Bucket(class) }

func (n Names) RepositoryPath(region string, class edge.Class) string {
	return region + dockerRegistryHost + "/" + n.project + "/" + n.Repository(class)
}

func (n Names) WorkloadAccount(class edge.Class) string {
	return string(n.namespace) + "-" + string(class)
}

func (n Names) WorkloadAccountEmail(class edge.Class) string {
	return n.WorkloadAccount(class) + "@" + n.project + accountDomain
}

func (n Names) Service(project, env, app, function string) (string, error) {
	parts := []string{string(n.namespace), naming.Sanitize(project), naming.Sanitize(env), naming.Sanitize(app)}
	if function != app {
		parts = append(parts, images.FunctionRoute(app, function))
	}
	service := strings.Join(parts, "-") + "-" + serviceHash(string(n.namespace), project, env, app, function)
	if len(service) > maxServiceName {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the Cloud Run service %s would be named %s, which is %d characters and Cloud Run builds a url from %d: "+
				"a service is named for the namespace, the project, the environment and the app it serves, "+
				"and ends in %d characters of a hash of the four, because every one of them may contain a dash and a name joined by dashes alone would read two ways.\n"+
				"Name a shorter namespace in %s, a shorter project slug, or a shorter app",
			app, service, len(service), maxServiceName, serviceHashLen, provider.NamespaceEnvVar)
	}
	return service, nil
}

const functionSuffix = "fn"

var cloudRunService = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

func (n Names) PreviewService(label, app, function string) (string, error) {
	service := label
	if function != app {
		service = strings.Join([]string{label, images.FunctionRoute(app, function), functionSuffix}, naming.FieldSeparator)
	}
	if len(service) > edge.PreviewLabelMaxLen || !cloudRunService.MatchString(service) {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the preview %s would be served by a Cloud Run service named %q, which Cloud Run will not take: "+
				"a service on a shared preview wildcard is named the label its hostname begins with, "+
				"because the load balancer hands the whole label to Cloud Run to find it, "+
				"and Cloud Run takes at most %d characters of lowercase letters, digits and dashes, "+
				"starting with a letter and ending with a letter or a digit.\n"+
				"This one is %s = %d characters. Shorten one of them and deploy again",
			app, service, edge.PreviewLabelMaxLen, edge.LabelParts(service), len(service))
	}
	return service, nil
}

func serviceHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:serviceHashLen]
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

func (n Names) PassphraseSecret(class edge.Class) string {
	return string(n.namespace) + "-" + string(class) + passphraseSuffix
}

func (n Names) fit() error {
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		for _, bucket := range []string{n.StateBucket(class), n.Bucket(class)} {
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
	if account := n.WorkloadAccount(longestClass); len(account) > maxAccountID {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s service account this bootstrap names is %d characters and Google takes %d: "+
				"every app in the %s class runs as it, so the class is part of its name.\n"+
				"Name a shorter namespace in %s",
			account, len(account), maxAccountID, longestClass, provider.NamespaceEnvVar)
	}
	return nil
}
