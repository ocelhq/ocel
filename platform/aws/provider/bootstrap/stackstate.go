package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type StackRecord struct {
	Edge       edge.StackState    `json:"edge"`
	Production domains.Settlement `json:"production,omitzero"`
}

func (r StackRecord) Empty() bool {
	return r.Edge.Empty() && r.Production.Empty()
}

const (
	StackStateParamPrefix        = "/ocel/rootstack/"
	PreviewStackStateParamPrefix = "/ocel/rootstack-preview/"
)

func stackStateParamPrefixFor(class string) (string, error) {
	switch class {
	case ClassProduction:
		return StackStateParamPrefix, nil
	case ClassPreview:
		return PreviewStackStateParamPrefix, nil
	default:
		return "", fmt.Errorf("edge stack state: unknown substrate class %q", class)
	}
}

func stackStateParamName(prefix, slug string) string {
	return prefix + slug
}

func stackStateParamDescription(class, slug string) string {
	return fmt.Sprintf(
		"Ocel: what the %s edge was left holding for project %q - the handles Ocel needs to recognise, update and eventually remove the root worker fronting this project. Written by every deploy of %q and read by the next deploy and by teardown. Delete it and Ocel loses track of edge resources it has already created for %q: they keep serving traffic, and teardown will not reclaim them.",
		class, slug, slug, slug,
	)
}

func WriteStackRecordFor(ctx context.Context, ssmClient SSMAPI, class, slug string, record StackRecord) error {
	prefix, err := stackStateParamPrefixFor(class)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal edge-stack state: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(stackStateParamName(prefix, slug)),
		Description: aws.String(stackStateParamDescription(class, slug)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write edge-stack state parameter: %w", err)
	}
	return nil
}

func ReadStackRecord(ctx context.Context, ssmClient SSMAPI, slug string) (StackRecord, error) {
	return ReadStackRecordFor(ctx, ssmClient, ClassProduction, slug)
}

func ReadStackRecordFor(ctx context.Context, ssmClient SSMAPI, class, slug string) (StackRecord, error) {
	prefix, err := stackStateParamPrefixFor(class)
	if err != nil {
		return StackRecord{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(stackStateParamName(prefix, slug)),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return StackRecord{}, nil
		}
		return StackRecord{}, fmt.Errorf("read edge-stack state parameter: %w", err)
	}
	var record StackRecord
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &record); err != nil {
		return StackRecord{}, fmt.Errorf("parse edge-stack state: %w", err)
	}
	return record, nil
}

type SSMPathAPI interface {
	GetParametersByPath(ctx context.Context, in *ssm.GetParametersByPathInput, optFns ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error)
}

func StackSlugsFor(ctx context.Context, api SSMPathAPI, class string) ([]string, error) {
	prefix, err := stackStateParamPrefixFor(class)
	if err != nil {
		return nil, err
	}
	var slugs []string
	var token *string
	for {
		out, err := api.GetParametersByPath(ctx, &ssm.GetParametersByPathInput{
			Path:      aws.String(prefix),
			NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list edge-stack state parameters: %w", err)
		}
		for _, param := range out.Parameters {
			if slug := strings.TrimPrefix(aws.ToString(param.Name), prefix); slug != "" {
				slugs = append(slugs, slug)
			}
		}
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			break
		}
		token = out.NextToken
	}
	slices.Sort(slugs)
	return slugs, nil
}

func DeleteStackRecord(ctx context.Context, ssmClient SSMAPI, slug string) error {
	return DeleteStackRecordFor(ctx, ssmClient, ClassProduction, slug)
}

func DeleteStackRecordFor(ctx context.Context, ssmClient SSMAPI, class, slug string) error {
	prefix, err := stackStateParamPrefixFor(class)
	if err != nil {
		return err
	}
	if _, err := ssmClient.DeleteParameter(ctx, &ssm.DeleteParameterInput{
		Name: aws.String(stackStateParamName(prefix, slug)),
	}); err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("delete edge-stack state parameter: %w", err)
	}
	return nil
}
