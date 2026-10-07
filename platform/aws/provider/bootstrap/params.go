package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ParamGroupName = "parameters"

	kindParameter = "AWS::SSM::Parameter"
	kindAccessKey = "AWS::IAM::AccessKey"

	paramCurrent    = "already current"
	valuesDrifted   = "what the edge hands back differs from what is stored"
	keyGone         = "the access key it records is no longer on the user"
	keyUnrecorded   = "it records no access key"
	keyStale        = "the access key it records is older than 90 days and is rotated"
	severedByRemove = "removing %s takes what the %s edge was reached through with it"

	passphraseStranded = "the only copy of the passphrase every Pulumi stack in this account was encrypted under; no bootstrap is left to need it"
	passphraseShared   = "the %s bootstrap is still installed and its Pulumi state is encrypted under it"
)

type ParamAPIs struct {
	SSM    SSMAPI
	IAM    IAMKeyAPI
	KMS    KeyAPI
	Region string
}

type EdgeAdoption struct {
	Kind     edge.Kind
	Adoption edge.Adoption
}

func PlanParameters(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, adoptions []EdgeAdoption, req Request) (provider.ChangeGroup, error) {
	group := provider.ChangeGroup{Kind: provider.ParameterGroupKind, Name: ParamGroupName}

	origin, err := planOriginSecret(ctx, apis.SSM, ns, tier, time.Now())
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	passphrase, err := paramPresence(ctx, apis.SSM, ns.PassphraseParamName())
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	group.Changes = append(group.Changes, origin, passphrase)

	var adopted []provider.Change
	for _, edging := range adoptions {
		changes, err := adoptionChanges(ctx, apis.SSM, ns, tier, edging.Kind, edging.Adoption)
		if err != nil {
			return provider.ChangeGroup{}, err
		}
		adopted = append(adopted, changes...)
	}
	written, err := featureParams(ctx, apis, ns, tier, req, req.Features, func(f feature) paramChanges { return f.afterPlan })
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	severed, err := featureParams(ctx, apis, ns, tier, req, Removing(req.Features, req.Remove), func(f feature) paramChanges { return f.dropPlan })
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	group.Changes = slices.Concat(group.Changes, adopted, written, severed)

	group.Action, group.Reason = provider.RollUp(group.Changes)
	return group, nil
}

type paramChanges func(context.Context, ParamAPIs, Namespace, environment.Tier, Request) ([]provider.Change, error)

func featureParams(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, req Request, named []string, hook func(feature) paramChanges) ([]provider.Change, error) {
	var changes []provider.Change
	for _, f := range featureRegistry {
		plan := hook(f)
		if plan == nil || !slices.Contains(named, f.name) {
			continue
		}
		planned, err := plan(ctx, apis, ns, tier, req)
		if err != nil {
			return nil, err
		}
		changes = append(changes, planned...)
	}
	return changes, nil
}

func PlanParameterRemoval(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, sharedPassphrase bool) (provider.ChangeGroup, error) {
	group := provider.ChangeGroup{Kind: provider.ParameterGroupKind, Name: ParamGroupName}

	names, err := TierParamNames(ns, tier)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	present, err := paramsPresent(ctx, apis.SSM, append(slices.Clone(names), ns.PassphraseParamName()))
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	for _, name := range names {
		if present[name] {
			group.Changes = append(group.Changes, provider.Change{
				Kind:   kindParameter,
				Name:   name,
				Action: provider.ActionDelete,
			})
		}
	}

	user, err := ns.EdgeUserNameFor(tier)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	keys, err := liveAccessKeys(ctx, apis.IAM, user)
	if err != nil {
		return provider.ChangeGroup{}, err
	}
	for _, id := range keys {
		group.Changes = append(group.Changes, provider.Change{
			Kind:   kindAccessKey,
			Name:   user + "/" + id,
			Action: provider.ActionDelete,
		})
	}

	passphrase := plannedPassphraseRemoval(present[ns.PassphraseParamName()], ns, tier, sharedPassphrase)
	if passphrase.Name != "" {
		group.Changes = append(group.Changes, passphrase)
	}

	group.Action = provider.ActionKeep
	for _, change := range group.Changes {
		if change.Action == provider.ActionDelete {
			group.Action = provider.ActionDelete
			break
		}
	}
	return group, nil
}

func plannedPassphraseRemoval(present bool, ns Namespace, tier environment.Tier, shared bool) provider.Change {
	if !present {
		return provider.Change{}
	}
	if !shared {
		return provider.Change{
			Kind:   kindParameter,
			Name:   ns.PassphraseParamName(),
			Action: provider.ActionDelete,
			Reason: passphraseStranded,
		}
	}
	return provider.Change{
		Kind:   kindParameter,
		Name:   ns.PassphraseParamName(),
		Action: provider.ActionKeep,
		Reason: fmt.Sprintf(passphraseShared, tier.Sibling()),
	}
}

func adoptionChanges(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier, kind edge.Kind, adoption edge.Adoption) ([]provider.Change, error) {
	if len(adoption.Values) == 0 && len(adoption.Offers) == 0 {
		return nil, nil
	}
	names, err := edgeNamesFor(ns, tier, kind)
	if err != nil {
		return nil, err
	}

	var changes []provider.Change
	if len(adoption.Values) > 0 {
		stored, err := ReadEdgeValues(ctx, ssmClient, ns, tier, kind)
		if err != nil {
			return nil, err
		}
		change := provider.Change{Kind: kindParameter, Name: names.valuesParam}
		switch {
		case stored == nil:
			change.Action = provider.ActionCreate
		case maps.Equal(stored, adoption.Values):
			change.Action, change.Reason = provider.ActionKeep, paramCurrent
		default:
			change.Action, change.Reason = provider.ActionUpdate, valuesDrifted
		}
		changes = append(changes, change)
	}

	for _, adopted := range []struct {
		offer  edge.OfferKind
		params []string
	}{
		{edge.OfferCacheStore, []string{names.cacheStoreParam}},
		{edge.OfferReleasesStore, []string{names.releasesStoreParam}},
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

func paramPresence(ctx context.Context, ssmClient SSMAPI, name string) (provider.Change, error) {
	present, err := paramPresent(ctx, ssmClient, name)
	if err != nil {
		return provider.Change{}, err
	}
	change := provider.Change{Kind: kindParameter, Name: name, Action: provider.ActionCreate}
	if present {
		change.Action, change.Reason = provider.ActionKeep, paramCurrent
	}
	return change, nil
}

func paramsPresent(ctx context.Context, api SSMBatchAPI, names []string) (map[string]bool, error) {
	found, err := getParameters(ctx, api, names)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(found))
	for name := range found {
		present[name] = true
	}
	return present, nil
}

func paramPresent(ctx context.Context, ssmClient SSMAPI, name string) (bool, error) {
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

func edgeKeyLive(ctx context.Context, iamClient IAMKeyAPI, userName, recorded string) (bool, error) {
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
