package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	ParamGroupName = "parameters"

	kindParameter = "AWS::SSM::Parameter"
	kindAccessKey = "AWS::IAM::AccessKey"

	paramCurrent    = "already current"
	valuesDrifted   = "what the edge hands back differs from what stands"
	keyGone         = "the access key it records is no longer on the user"
	keyUnrecorded   = "it records no access key"
	severedByRemove = "removing %s takes what the %s edge was reached through with it"
)

type ParamAPIs struct {
	SSM SSMAPI
	IAM IAMKeyAPI
}

func PlanParameters(ctx context.Context, apis ParamAPIs, class string, kind edge.Kind, adoption edge.Adoption, req Request) (providerkit.ChangeGroup, error) {
	group := providerkit.ChangeGroup{Kind: providerkit.ParameterGroupKind, Name: ParamGroupName}

	origin, err := OriginSecretParamFor(class)
	if err != nil {
		return providerkit.ChangeGroup{}, err
	}
	for _, name := range []string{origin, PassphraseParamName} {
		change, err := paramPresence(ctx, apis.SSM, name)
		if err != nil {
			return providerkit.ChangeGroup{}, err
		}
		group.Changes = append(group.Changes, change)
	}

	adopted, err := adoptionChanges(ctx, apis.SSM, class, kind, adoption, req)
	if err != nil {
		return providerkit.ChangeGroup{}, err
	}
	credentials, err := plannedEdgeCredentials(ctx, apis, class, req)
	if err != nil {
		return providerkit.ChangeGroup{}, err
	}
	severed, err := plannedCloudflareSever(ctx, apis, class, req)
	if err != nil {
		return providerkit.ChangeGroup{}, err
	}
	group.Changes = slices.Concat(group.Changes, adopted, credentials, severed)

	group.Action, group.Reason = providerkit.RollUp(group.Changes)
	return group, nil
}

func adoptionChanges(ctx context.Context, ssmClient SSMAPI, class string, kind edge.Kind, adoption edge.Adoption, req Request) ([]providerkit.Change, error) {
	if droppingEdge(kind, req) || (len(adoption.Values) == 0 && len(adoption.Offers) == 0) {
		return nil, nil
	}
	names, err := edgeNamesFor(class, kind)
	if err != nil {
		return nil, err
	}

	var changes []providerkit.Change
	if len(adoption.Values) > 0 {
		stored, err := ReadEdgeValues(ctx, ssmClient, class, kind)
		if err != nil {
			return nil, err
		}
		change := providerkit.Change{Kind: kindParameter, Name: names.valuesParam}
		switch {
		case stored == nil:
			change.Action = providerkit.ActionCreate
		case maps.Equal(stored, adoption.Values):
			change.Action, change.Reason = providerkit.ActionKeep, paramCurrent
		default:
			change.Action, change.Reason = providerkit.ActionUpdate, valuesDrifted
		}
		changes = append(changes, change)
	}

	for _, adopted := range []struct {
		offer  edge.OfferKind
		params []string
	}{
		{edge.OfferCacheStore, []string{names.cacheStoreParam}},
		{edge.OfferDeploymentsStore, []string{names.deploymentsStoreParam}},
		{edge.OfferISRWriter, []string{names.isrWriterParam, names.isrWriterSeedParam}},
	} {
		if !slices.Contains(adoption.Offers, adopted.offer) {
			continue
		}
		for _, name := range adopted.params {
			change, err := paramPresence(ctx, ssmClient, name)
			if err != nil {
				return nil, err
			}
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func paramPresence(ctx context.Context, ssmClient SSMAPI, name string) (providerkit.Change, error) {
	held, err := paramHeld(ctx, ssmClient, name)
	if err != nil {
		return providerkit.Change{}, err
	}
	change := providerkit.Change{Kind: kindParameter, Name: name, Action: providerkit.ActionCreate}
	if held {
		change.Action, change.Reason = providerkit.ActionKeep, paramCurrent
	}
	return change, nil
}

func paramHeld(ctx context.Context, ssmClient SSMAPI, name string) (bool, error) {
	if _, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	}); err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", name, err)
	}
	return true, nil
}

func edgeKeyStanding(ctx context.Context, iamClient IAMKeyAPI, userName, recorded string) (bool, error) {
	if recorded == "" {
		return false, nil
	}
	ids, err := liveAccessKeys(ctx, iamClient, userName)
	if err != nil {
		return false, err
	}
	return slices.Contains(ids, recorded), nil
}

func liveAccessKeys(ctx context.Context, iamClient IAMKeyAPI, userName string) ([]string, error) {
	out, err := iamClient.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(userName)})
	if err != nil {
		var noUser *iamtypes.NoSuchEntityException
		if errors.As(err, &noUser) {
			return nil, nil
		}
		return nil, fmt.Errorf("list access keys for %s: %w", userName, err)
	}
	ids := make([]string, 0, len(out.AccessKeyMetadata))
	for _, key := range out.AccessKeyMetadata {
		ids = append(ids, aws.ToString(key.AccessKeyId))
	}
	return ids, nil
}
