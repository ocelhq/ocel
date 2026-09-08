package providerkit

import (
	"fmt"
	"os"
)

// Namespace is the prefix every name a bootstrap stands up is derived from, so
// two bootstraps of the same project never name the same resource.
type Namespace string

const (
	// DefaultNamespace is the namespace a run that names none deploys under.
	DefaultNamespace Namespace = "ocel"

	// MaxNamespaceLength is the longest namespace every provider can derive
	// its names from and still fit the shortest limit they land under.
	MaxNamespaceLength = 44

	// NamespaceEnvVar names the namespace a run deploys under.
	NamespaceEnvVar = "OCEL_NAMESPACE"
)

// ParseNamespace reads the namespace a run deploys under, defaulting to
// [DefaultNamespace] when nothing names one.
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

// NamespaceFromEnv reads the namespace [NamespaceEnvVar] names, refusing in the
// provider's own voice when it names one no cloud would take.
func NamespaceFromEnv() (Namespace, error) {
	ns, err := ParseNamespace(os.Getenv(NamespaceEnvVar))
	if err != nil {
		return "", Refuse(CodeInvalid, "%s: %s", NamespaceEnvVar, err.Error())
	}
	return ns, nil
}

// String returns the namespace as the prefix names are derived from.
func (n Namespace) String() string { return string(n) }
