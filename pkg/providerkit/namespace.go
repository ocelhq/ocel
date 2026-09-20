package providerkit

import (
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/naming"
)

type Namespace string

const DefaultNamespace Namespace = "ocel"

const (
	MaxNamespaceLength = 44
	NamespaceEnvVar    = "OCEL_NAMESPACE"
)

func ParseNamespace(given string) (Namespace, error) {
	if given == "" {
		return DefaultNamespace, nil
	}
	if len(given) > MaxNamespaceLength {
		return "", fmt.Errorf("namespace %q is %d characters; every name a bootstrap derives from it has to fit the shortest limit it lands under, which leaves %d", given, len(given), MaxNamespaceLength)
	}
	if err := naming.Validate("namespace", given); err != nil || given[0] < 'a' || given[0] > 'z' {
		return "", fmt.Errorf("namespace %q is not the field every name derived from it carries: start with a lowercase letter, carry only lowercase letters, digits and single dashes, and end with a letter or digit", given)
	}
	return Namespace(given), nil
}

func NamespaceFromEnv() (Namespace, error) {
	ns, err := ParseNamespace(os.Getenv(NamespaceEnvVar))
	if err != nil {
		return "", Refuse(CodeInvalid, "%s: %s", NamespaceEnvVar, err.Error())
	}
	return ns, nil
}

func (n Namespace) String() string { return string(n) }
