package bootstrap

import (
	"fmt"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Namespace string

const (
	DefaultNamespace Namespace = "ocel"

	MaxNamespaceLength = 44

	NamespaceEnvVar = "OCEL_NAMESPACE"
)

func ParseNamespace(given string) (Namespace, error) {
	if given == "" {
		return DefaultNamespace, nil
	}
	if len(given) > MaxNamespaceLength {
		return "", fmt.Errorf("namespace %q is %d characters; every name a bootstrap derives from it has to fit the shortest AWS limit it lands under, which leaves %d", given, len(given), MaxNamespaceLength)
	}
	for i := range len(given) {
		c := given[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return "", fmt.Errorf("namespace %q is not a name AWS accepts everywhere it lands: start with a lowercase letter and carry only lowercase letters, digits and dashes", given)
		}
	}
	return Namespace(given), nil
}

func (n Namespace) String() string { return string(n) }

func suffixed(class, base string) string {
	if class == ClassPreview {
		return base + "-preview"
	}
	return base
}

func (n Namespace) CoreStackName() string { return string(n) + "-bootstrap" }

func (n Namespace) StackNameFor(class string) (string, error) {
	switch class {
	case ClassProduction, ClassPreview:
		return suffixed(class, n.CoreStackName()), nil
	default:
		return "", fmt.Errorf("bootstrap: unknown class %q", class)
	}
}

func (n Namespace) featureStackName(feature, class string) string {
	return suffixed(class, n.CoreStackName()+"-"+feature)
}

func (n Namespace) FeatureStackName(name, class string) string {
	if _, ok := featureNamed(name); !ok {
		return name
	}
	return n.featureStackName(name, class)
}

func (n Namespace) paramRoot() string { return "/" + string(n) }

func (n Namespace) PassphraseParamName() string { return n.paramRoot() + "/pulumi/passphrase" }

func (n Namespace) stackRecordRoot() string { return n.paramRoot() + "/rootstack" }

func (n Namespace) EdgeUserNameFor(class string) (string, error) {
	switch class {
	case ClassProduction, ClassPreview:
		return suffixed(class, string(n)+"-edge"), nil
	default:
		return "", fmt.Errorf("edge: unknown class %q", class)
	}
}

func (n Namespace) AppBoundaryNameFor(class string) string {
	return suffixed(class, string(n)+"-app-boundary")
}

func (n Namespace) OriginSecretParamFor(class string) (string, error) {
	switch class {
	case ClassProduction:
		return n.paramRoot() + "/origin/secret", nil
	case ClassPreview:
		return n.paramRoot() + "/origin/secret-preview", nil
	default:
		return "", fmt.Errorf("edge: unknown class %q", class)
	}
}

func (n Namespace) EdgeParamPrefix(class string, kind edge.Kind) (string, error) {
	if kind == "" {
		return "", fmt.Errorf("edge: the %s bootstrap's edge parameters are namespaced by edge kind, and this run names none", class)
	}
	switch class {
	case ClassProduction, ClassPreview:
		return suffixed(class, n.paramRoot()+"/edge/"+string(kind)), nil
	default:
		return "", fmt.Errorf("edge: unknown class %q", class)
	}
}

func (n Namespace) varsKeyAliasFor(class string) string {
	return "alias/" + string(n) + "-vars-" + class
}

func (n Namespace) EdgeInvokeRoleName(class edge.Class) string {
	return n.edgeSetName("edge-invoke", class)
}

func (n Namespace) EdgeNotFoundAPIName(class edge.Class) string {
	return string(n) + "-not-found-" + string(class)
}

func (n Namespace) EdgeRoutesStoreName(class edge.Class) string {
	return n.edgeSetName("routes", class)
}

func (n Namespace) EdgeResolverName(class edge.Class) string {
	return n.edgeSetName("resolver", class)
}

func (n Namespace) edgeCachePolicyName(class edge.Class) string {
	return n.edgeSetName("cache", class)
}

func (n Namespace) edgeHeadersPolicyName(class edge.Class) string {
	return n.edgeSetName("headers", class)
}

func (n Namespace) edgeAssetAccessName(class edge.Class) string {
	return n.edgeSetName("assets", class)
}

func (n Namespace) edgeSetName(what string, class edge.Class) string {
	return suffixed(string(class), string(n)+"-"+what)
}

func (n Namespace) revalidateQueueNames(class string) (queue, dlq string) {
	base := suffixed(class, string(n)+"-revalidate")
	return base + ".fifo", base + "-dlq.fifo"
}

func (n Namespace) policyName(what string) string { return string(n) + "-" + what }

func (n Namespace) changeSetName() string {
	return fmt.Sprintf("%s-%d", n, time.Now().UnixNano())
}
