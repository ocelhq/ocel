package providerkit

import (
	"fmt"
	"os"
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
	for i := range len(given) {
		c := given[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return "", fmt.Errorf("namespace %q is not a name every cloud accepts everywhere it lands: start with a lowercase letter and carry only lowercase letters, digits and dashes", given)
		}
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
