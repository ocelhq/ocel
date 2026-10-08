package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

const batchDeleteImages = 100

func Sweep(ctx context.Context, api ECRAPI, target provider.RegistryTarget, imageRef string, kept map[string]bool, pushedBefore time.Time) ([]string, error) {
	repository, err := repositoryOf(target, imageRef)
	if err != nil {
		return nil, err
	}
	var doomed []string
	var token *string
	for {
		listed, err := api.DescribeImages(ctx, &ecr.DescribeImagesInput{
			RepositoryName: aws.String(repository),
			Filter:         &ecrtypes.DescribeImagesFilter{TagStatus: ecrtypes.TagStatusTagged},
			NextToken:      token,
		})
		var gone *ecrtypes.RepositoryNotFoundException
		if errors.As(err, &gone) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list the images of %s in this account's ECR: %w", repository, err)
		}
		for _, detail := range listed.ImageDetails {
			if detail.ImagePushedAt == nil || detail.ImagePushedAt.After(pushedBefore) {
				continue
			}
			for _, tag := range detail.ImageTags {
				if !kept[target.Server+"/"+repository+":"+tag] {
					doomed = append(doomed, tag)
				}
			}
		}
		if token = listed.NextToken; token == nil {
			break
		}
	}
	return deleteTags(ctx, api, target, repository, doomed)
}

func Release(ctx context.Context, api ECRAPI, target provider.RegistryTarget, imageRefs []string, kept map[string]bool) ([]string, error) {
	tagged := map[string][]string{}
	for _, imageRef := range imageRefs {
		if kept[imageRef] {
			continue
		}
		repository, err := repositoryOf(target, imageRef)
		if err != nil {
			return nil, err
		}
		tag, err := tagOf(imageRef)
		if err != nil {
			return nil, err
		}
		tagged[repository] = append(tagged[repository], tag)
	}
	var removed []string
	var errs []error
	for _, repository := range slices.Sorted(maps.Keys(tagged)) {
		gone, err := deleteTags(ctx, api, target, repository, tagged[repository])
		removed = append(removed, gone...)
		errs = append(errs, err)
	}
	return removed, errors.Join(errs...)
}

func deleteTags(ctx context.Context, api ECRAPI, target provider.RegistryTarget, repository string, tags []string) ([]string, error) {
	var removed []string
	for batch := range slices.Chunk(tags, batchDeleteImages) {
		ids := make([]ecrtypes.ImageIdentifier, 0, len(batch))
		for _, tag := range batch {
			ids = append(ids, ecrtypes.ImageIdentifier{ImageTag: aws.String(tag)})
		}
		out, err := api.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{RepositoryName: aws.String(repository), ImageIds: ids})
		var gone *ecrtypes.RepositoryNotFoundException
		if errors.As(err, &gone) {
			return removed, nil
		}
		if err != nil {
			return removed, fmt.Errorf("remove images from %s in this account's ECR: %w", repository, err)
		}
		for _, id := range out.ImageIds {
			removed = append(removed, target.Server+"/"+repository+":"+aws.ToString(id.ImageTag))
		}
		for _, failure := range out.Failures {
			if failure.FailureCode != ecrtypes.ImageFailureCodeImageNotFound {
				return removed, fmt.Errorf("remove an image of %s from this account's ECR: %s: %s",
					repository, failure.FailureCode, aws.ToString(failure.FailureReason))
			}
		}
	}
	return removed, nil
}

func Reconcile(ctx context.Context, api ECRAPI, imageRef string, standing map[string]bool, pushedBefore time.Time) ([]string, error) {
	target, err := Resolve(ctx, api)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(imageRef, target.Server+"/") {
		return nil, nil
	}
	kept := maps.Clone(standing)
	if kept == nil {
		kept = map[string]bool{}
	}
	kept[imageRef] = true
	return Sweep(ctx, api, target, imageRef, kept, pushedBefore)
}

func Forget(ctx context.Context, api ECRAPI, imageRefs []string, standing map[string]bool) ([]string, error) {
	target, err := Resolve(ctx, api)
	if err != nil {
		return nil, err
	}
	var ours []string
	for _, imageRef := range imageRefs {
		if strings.HasPrefix(imageRef, target.Server+"/") {
			ours = append(ours, imageRef)
		}
	}
	return Release(ctx, api, target, ours, standing)
}
