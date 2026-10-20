package account

import (
	"fmt"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Namespace provider.Namespace

const bootstrapWord = "bootstrap"

func knownTier(what string, tier environment.Tier) error {
	switch tier {
	case environment.TierProduction, environment.TierPreview:
		return nil
	default:
		return fmt.Errorf("%s: unknown tier %q", what, tier)
	}
}

func (n Namespace) name(tier environment.Tier, words ...string) string {
	return naming.OcelName(string(n), tier, words...)
}

func (n Namespace) path(category string, tier environment.Tier, rest ...string) string {
	return strings.Join(append([]string{n.paramRoot(), category, naming.TierWord(tier)}, rest...), naming.PathSeparator)
}

func (n Namespace) BootstrapBucketStem(tier environment.Tier, class naming.StorageClass) string {
	return naming.OcelName(string(n), tier, string(class))
}

func (n Namespace) BootstrapBucketName(tier environment.Tier, class naming.StorageClass, tag string) string {
	return n.BootstrapBucketStem(tier, class) + naming.WordSeparator + tag
}

func (n Namespace) StackNameFor(tier environment.Tier) (string, error) {
	if err := knownTier("bootstrap", tier); err != nil {
		return "", err
	}
	return n.name(tier, bootstrapWord), nil
}

func (n Namespace) partStackName(part string, tier environment.Tier) string {
	return n.name(tier, bootstrapWord, part)
}

func (n Namespace) PartStackName(name string, tier environment.Tier) string {
	if _, ok := partNamed(name); !ok {
		return name
	}
	return n.partStackName(name, tier)
}

func (n Namespace) paramRoot() string { return naming.PathSeparator + naming.NamespaceField(string(n)) }

func (n Namespace) PassphraseParamFor(tier environment.Tier) string {
	return n.path("pulumi", tier, "passphrase")
}

func (n Namespace) KVTokenRoot() string { return n.paramRoot() + "/kv" }

func (n Namespace) SigningKeyRoot() string { return naming.NamespaceField(string(n)) + "/realtime" }

func (n Namespace) EdgeUserNameFor(tier environment.Tier) (string, error) {
	if err := knownTier("edge", tier); err != nil {
		return "", err
	}
	return n.name(tier, "edge"), nil
}

func (n Namespace) AppBoundaryNameFor(tier environment.Tier) string {
	return n.name(tier, "boundary")
}

func (n Namespace) OriginSecretParamFor(tier environment.Tier) (string, error) {
	if err := knownTier("edge", tier); err != nil {
		return "", err
	}
	return n.path("origin", tier, "secret"), nil
}

func (n Namespace) EdgeParamPrefix(tier environment.Tier, kind edge.Kind) (string, error) {
	if kind == "" {
		return "", fmt.Errorf("edge: the %s bootstrap's edge parameters are namespaced by edge kind, and this run names none", tier)
	}
	if err := knownTier("edge", tier); err != nil {
		return "", err
	}
	return n.path("edge", tier, string(kind)), nil
}

func (n Namespace) variablesKeyAliasFor(tier environment.Tier) string {
	return "alias/" + n.name(tier, "variables")
}

func (n Namespace) rolePathFor(tier environment.Tier, purpose string) string {
	return naming.PathSeparator + strings.Join([]string{naming.NamespaceField(string(n)), naming.TierWord(tier), purpose}, naming.PathSeparator) + naming.PathSeparator
}

func (n Namespace) bootstrapRolePathFor(tier environment.Tier) string {
	return n.rolePathFor(tier, "bootstrap")
}

func (n Namespace) AppRolePathFor(tier environment.Tier) string {
	return n.rolePathFor(tier, "app")
}

func (n Namespace) EdgeInvokeRoleName(tier environment.Tier) string {
	return n.edgeSetName("edge-invoke", tier)
}

func (n Namespace) EdgeRoutesStoreName(tier environment.Tier) string {
	return n.edgeSetName("routes", tier)
}

func (n Namespace) EdgeResolverName(tier environment.Tier) string {
	return n.edgeSetName("resolver", tier)
}

func (n Namespace) EdgeEmptyBodyName(tier environment.Tier) string {
	return n.edgeSetName("empty-body", tier)
}

func (n Namespace) EdgeCachePolicyName(tier environment.Tier) string {
	return n.edgeSetName("cache", tier)
}

func (n Namespace) EdgeHeadersPolicyName(tier environment.Tier) string {
	return n.edgeSetName("headers", tier)
}

func (n Namespace) EdgeAssetAccessName(tier environment.Tier) string {
	return n.edgeSetName("assets", tier)
}

func (n Namespace) edgeSetName(what string, tier environment.Tier) string {
	return n.name(tier, what)
}

func (n Namespace) PolicyName(what string) string {
	return naming.MustUntieredOcelName(string(n), what)
}

func (n Namespace) ChangeSetNameFor(stackName string) string {
	return fmt.Sprintf("%s-%d", stackName, time.Now().UnixNano())
}
