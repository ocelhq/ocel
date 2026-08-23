package control

import (
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func removals(class string, edgeKind edge.Kind, deployed bootstrap.Deployed, sharedPassphrase bool) ([]edge.Surface, error) {
	stackName, err := bootstrap.StackNameFor(class)
	if err != nil {
		return nil, err
	}
	userName, err := bootstrap.EdgeUserNameFor(class)
	if err != nil {
		return nil, err
	}
	params, err := bootstrap.ClassParamNames(class)
	if err != nil {
		return nil, err
	}

	surfaces := []edge.Surface{{
		Kind:   "edge bootstrap",
		Name:   string(edgeKind),
		Action: edge.SurfaceDelete,
		Reason: fmt.Sprintf("every worker, cache store and credential the %s edge stood up for the %s bootstrap", edgeKind, class),
		Slow:   true,
	}}

	if deployed.Present {
		for _, bucket := range []struct{ name, reason string }{
			{deployed.StateBucket, "the Pulumi state of every stack this bootstrap deployed, all versions of it; nothing can describe or remove those resources afterwards"},
			{deployed.ArtifactBucket, "the function code staged for this bootstrap"},
			{deployed.AssetBucket, "every build's static assets, prerender fallbacks and edge fetch cache"},
		} {
			if bucket.name == "" {
				continue
			}
			surfaces = append(surfaces, edge.Surface{
				Kind:   "bucket",
				Name:   bucket.name,
				Action: edge.SurfaceDelete,
				Reason: bucket.reason + "; emptied object by object first",
				Slow:   true,
			})
		}
		if deployed.StateTable != "" {
			surfaces = append(surfaces, edge.Surface{
				Kind:   "state table",
				Name:   deployed.StateTable,
				Action: edge.SurfaceDelete,
				Reason: "the stack index teardown walks and the ISR tag clock the edge reads",
			})
		}
		if deployed.VarsTable != "" {
			surfaces = append(surfaces, edge.Surface{
				Kind:   "variable store",
				Name:   deployed.VarsTable,
				Action: edge.SurfaceDelete,
				Reason: fmt.Sprintf("every %s variable value in this account, and the key they are encrypted under", class),
			})
		}
		if deployed.Features.Has(bootstrap.FeatureCloudflareEdge) {
			surfaces = append(surfaces, edge.Surface{
				Kind:   "edge reader",
				Name:   userName,
				Action: edge.SurfaceDelete,
				Reason: "the IAM user the edge signs its calls into this account with, and its access key",
			})
		}
		order, err := bootstrap.FeatureDeleteOrder(deployed.Features.Names())
		if err != nil {
			return nil, err
		}
		for _, feature := range order {
			surfaces = append(surfaces, edge.Surface{
				Kind:   "feature stack",
				Name:   bootstrap.FeatureStackName(feature, class),
				Action: edge.SurfaceDelete,
				Reason: fmt.Sprintf("the CloudFormation stack carrying the %s feature of this bootstrap", feature),
			})
		}
		surfaces = append(surfaces, edge.Surface{
			Kind:   "bootstrap stack",
			Name:   stackName,
			Action: edge.SurfaceDelete,
			Reason: "the CloudFormation stack holding the core every feature above was built on",
		})
	}

	for _, name := range params {
		surfaces = append(surfaces, edge.Surface{
			Kind:   "parameter",
			Name:   name,
			Action: edge.SurfaceDelete,
			Reason: "a handle this bootstrap stored; nothing reads it once the bootstrap is gone",
		})
	}

	passphrase := edge.Surface{
		Kind:   "parameter",
		Name:   bootstrap.PassphraseParamName,
		Action: edge.SurfaceDelete,
		Reason: "the only copy of the passphrase every Pulumi stack in this account is encrypted under",
	}
	if sharedPassphrase {
		sibling, err := bootstrap.SiblingClassOf(class)
		if err != nil {
			return nil, err
		}
		passphrase.Action = edge.SurfaceKeep
		passphrase.Reason = fmt.Sprintf("the %s bootstrap still stands and its Pulumi state is encrypted under it", sibling)
	}
	return append(surfaces, passphrase), nil
}
