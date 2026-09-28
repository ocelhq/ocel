package bootstrap

import (
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Namespace provider.Namespace

func suffixed(tier, base string) string {
	if tier == TierPreview {
		return base + "-preview"
	}
	return base
}

func (n Namespace) CoreStackName() string { return string(n) + "-bootstrap" }

func (n Namespace) StackNameFor(tier string) (string, error) {
	switch tier {
	case TierProduction, TierPreview:
		return suffixed(tier, n.CoreStackName()), nil
	default:
		return "", fmt.Errorf("bootstrap: unknown tier %q", tier)
	}
}

func (n Namespace) runtimeStackName(tier string) string {
	return suffixed(tier, n.CoreStackName()+"-runtime")
}

func (n Namespace) featureStackName(feature, tier string) string {
	return suffixed(tier, n.CoreStackName()+"-"+feature)
}

func (n Namespace) FeatureStackName(name, tier string) string {
	if _, ok := featureNamed(name); !ok {
		return name
	}
	return n.featureStackName(name, tier)
}

func (n Namespace) paramRoot() string { return "/" + string(n) }

func (n Namespace) PassphraseParamName() string { return n.paramRoot() + "/pulumi/passphrase" }

func (n Namespace) stackRecordRoot() string { return n.paramRoot() + "/rootstack" }

func (n Namespace) EdgeUserNameFor(tier string) (string, error) {
	switch tier {
	case TierProduction, TierPreview:
		return suffixed(tier, string(n)+"-edge"), nil
	default:
		return "", fmt.Errorf("edge: unknown tier %q", tier)
	}
}

func (n Namespace) AppBoundaryNameFor(tier string) string {
	return suffixed(tier, string(n)+"-app-boundary")
}

func (n Namespace) OriginSecretParamFor(tier string) (string, error) {
	switch tier {
	case TierProduction:
		return n.paramRoot() + "/origin/secret", nil
	case TierPreview:
		return n.paramRoot() + "/origin/secret-preview", nil
	default:
		return "", fmt.Errorf("edge: unknown tier %q", tier)
	}
}

func (n Namespace) EdgeParamPrefix(tier string, kind edge.Kind) (string, error) {
	if kind == "" {
		return "", fmt.Errorf("edge: the %s bootstrap's edge parameters are namespaced by edge kind, and this run names none", tier)
	}
	switch tier {
	case TierProduction, TierPreview:
		return suffixed(tier, n.paramRoot()+"/edge/"+string(kind)), nil
	default:
		return "", fmt.Errorf("edge: unknown tier %q", tier)
	}
}

func (n Namespace) varsKeyAliasFor(tier string) string {
	return "alias/" + string(n) + "-vars-" + tier
}

func (n Namespace) EdgeInvokeRoleName(tier environment.Tier) string {
	return n.edgeSetName("edge-invoke", tier)
}

func (n Namespace) EdgeNotFoundAPIName(tier environment.Tier) string {
	return string(n) + "-not-found-" + string(tier)
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

func (n Namespace) edgeCachePolicyName(tier environment.Tier) string {
	return n.edgeSetName("cache", tier)
}

func (n Namespace) edgeHeadersPolicyName(tier environment.Tier) string {
	return n.edgeSetName("headers", tier)
}

func (n Namespace) edgeAssetAccessName(tier environment.Tier) string {
	return n.edgeSetName("assets", tier)
}

func (n Namespace) edgeSetName(what string, tier environment.Tier) string {
	return suffixed(string(tier), string(n)+"-"+what)
}

func (n Namespace) revalidateQueueNames(tier string) (queue, dlq string) {
	base := suffixed(tier, string(n)+"-revalidate")
	return base + ".fifo", base + "-dlq.fifo"
}

func (n Namespace) envSourceSyncScheduleGroupName(tier string) string {
	return suffixed(tier, n.CoreStackName())
}

func (n Namespace) envSourceSyncScheduleName(tier string) string {
	return suffixed(tier, string(n)+"-env-sync")
}

func (n Namespace) PolicyName(what string) string { return string(n) + "-" + what }

func (n Namespace) ChangeSetNameFor(stackName string) string {
	return fmt.Sprintf("%s-%d", stackName, time.Now().UnixNano())
}
