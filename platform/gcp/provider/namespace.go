package gcp

import (
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	stateBucketSuffix    = "-state"
	passphraseSuffix     = "-pulumi-passphrase"
	runtimeAccountSuffix = "-run"
)

const (
	accountDomain      = ".iam.gserviceaccount.com"
	dockerRegistryHost = "-docker.pkg.dev"
)

const (
	maxBucketName = 63
	maxAccountID  = 30
	minDatabaseID = 4
)

var uuidLike = regexp.MustCompile(`[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}`)

type Names struct {
	namespace providerkit.Namespace
	project   string
}

func (n Names) Namespace() providerkit.Namespace { return n.namespace }

func (n Names) Bucket(class providerkit.Class) string {
	return string(n.namespace) + "-" + n.project + "-" + string(class)
}

func (n Names) StateBucket(class providerkit.Class) string {
	return n.Bucket(class) + stateBucketSuffix
}

func (n Names) Repository(class providerkit.Class) string { return n.Bucket(class) }

func (n Names) RepositoryPath(region string, class providerkit.Class) string {
	return region + dockerRegistryHost + "/" + n.project + "/" + n.Repository(class)
}

func (n Names) RuntimeAccount(class providerkit.Class) string {
	return string(n.namespace) + "-" + string(class) + runtimeAccountSuffix
}

func (n Names) RuntimeAccountEmail(class providerkit.Class) string {
	return n.RuntimeAccount(class) + "@" + n.project + accountDomain
}

func (n Names) Database() string { return string(n.namespace) }

func (n Names) KeyRing() string { return string(n.namespace) }

func (n Names) PassphraseSecret(class providerkit.Class) string {
	return string(n.namespace) + "-" + string(class) + passphraseSuffix
}

func (n Names) fit() error {
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for _, bucket := range []string{n.StateBucket(class), n.Bucket(class)} {
			if len(bucket) > maxBucketName {
				return providerkit.Refuse(providerkit.CodeInvalid,
					"the %s bucket this bootstrap names is %d characters and Cloud Storage takes %d: "+
						"namespace %q and project %q together leave nothing to cut.\n"+
						"Name a shorter namespace in %s, or deploy into a project with a shorter id",
					bucket, len(bucket), maxBucketName, n.namespace, n.project, providerkit.NamespaceEnvVar)
			}
		}
	}
	if len(n.Database()) < minDatabaseID {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the %q Firestore database this bootstrap names is %d characters and Firestore takes at least %d.\n"+
				"Name a longer namespace in %s",
			n.Database(), len(n.Database()), minDatabaseID, providerkit.NamespaceEnvVar)
	}
	if strings.HasSuffix(n.Database(), "-") {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the %q Firestore database this bootstrap names ends in a dash, and a Firestore database id ends in a letter or a digit.\n"+
				"Name a namespace in %s that ends in one",
			n.Database(), providerkit.NamespaceEnvVar)
	}
	if uuidLike.MatchString(n.Database()) {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the %q Firestore database this bootstrap names reads as a UUID, and a Firestore database id may not.\n"+
				"Name a namespace in %s that does not",
			n.Database(), providerkit.NamespaceEnvVar)
	}
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		if account := n.RuntimeAccount(class); len(account) > maxAccountID {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"the %s service account this bootstrap names is %d characters and Google takes %d: "+
					"every app in the %s class runs as it, so the class is part of its name.\n"+
					"Name a shorter namespace in %s",
				account, len(account), maxAccountID, class, providerkit.NamespaceEnvVar)
		}
	}
	return nil
}
