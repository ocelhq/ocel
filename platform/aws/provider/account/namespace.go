package account

import (
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Namespace provider.Namespace

func suffixed(tier environment.Tier, base string) string {
	if tier == environment.TierPreview {
		return base + "-preview"
	}
	return base
}

func (n Namespace) CoreStackName() string { return string(n) + "-bootstrap" }

func (n Namespace) StackNameFor(tier environment.Tier) (string, error) {
	switch tier {
	case environment.TierProduction, environment.TierPreview:
		return suffixed(tier, n.CoreStackName()), nil
	default:
		return "", fmt.Errorf("bootstrap: unknown tier %q", tier)
	}
}

func (n Namespace) partStackName(part string, tier environment.Tier) string {
	return suffixed(tier, n.CoreStackName()+"-"+part)
}

func (n Namespace) PartStackName(name string, tier environment.Tier) string {
	if _, ok := partNamed(name); !ok {
		return name
	}
	return n.partStackName(name, tier)
}

func (n Namespace) paramRoot() string { return "/" + string(n) }

func (n Namespace) PassphraseParamFor(tier environment.Tier) string {
	return suffixed(tier, n.paramRoot()+"/pulumi/passphrase")
}

func (n Namespace) KVTokenRoot() string { return n.paramRoot() + "/kv" }

func (n Namespace) SigningKeyRoot() string { return string(n) + "/realtime" }

func (n Namespace) EdgeUserNameFor(tier environment.Tier) (string, error) {
	switch tier {
	case environment.TierProduction, environment.TierPreview:
		return suffixed(tier, string(n)+"-edge"), nil
	default:
		return "", fmt.Errorf("edge: unknown tier %q", tier)
	}
}

func (n Namespace) AppBoundaryNameFor(tier environment.Tier) string {
	return suffixed(tier, string(n)+"-app-boundary")
}

func (n Namespace) OriginSecretParamFor(tier environment.Tier) (string, error) {
	switch tier {
	case environment.TierProduction:
		return n.paramRoot() + "/origin/secret", nil
	case environment.TierPreview:
		return n.paramRoot() + "/origin/secret-preview", nil
	default:
		return "", fmt.Errorf("edge: unknown tier %q", tier)
	}
}

func (n Namespace) EdgeParamPrefix(tier environment.Tier, kind edge.Kind) (string, error) {
	if kind == "" {
		return "", fmt.Errorf("edge: the %s bootstrap's edge parameters are namespaced by edge kind, and this run names none", tier)
	}
	switch tier {
	case environment.TierProduction, environment.TierPreview:
		return suffixed(tier, n.paramRoot()+"/edge/"+string(kind)), nil
	default:
		return "", fmt.Errorf("edge: unknown tier %q", tier)
	}
}

func (n Namespace) variablesKeyAliasFor(tier environment.Tier) string {
	return "alias/" + string(n) + "-variables-" + string(tier)
}

func (n Namespace) bootstrapRolePathFor(tier environment.Tier) string {
	return "/" + string(n) + "/" + string(tier) + "/bootstrap/"
}

func (n Namespace) AppRolePathFor(tier environment.Tier) string {
	return "/" + string(n) + "/" + string(tier) + "/app/"
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
	return suffixed(tier, string(n)+"-"+what)
}

func (n Namespace) PolicyName(what string) string { return string(n) + "-" + what }

func (n Namespace) ChangeSetNameFor(stackName string) string {
	return fmt.Sprintf("%s-%d", stackName, time.Now().UnixNano())
}
