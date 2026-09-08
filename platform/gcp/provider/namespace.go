package gcp

import (
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	stateBucketSuffix = "-state"
	passphraseSuffix  = "-pulumi-passphrase"
)

const (
	maxBucketName = 63
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
	return nil
}
