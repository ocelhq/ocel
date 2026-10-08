package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

const batchDeleteImages = 100

type Recorded func(ctx context.Context) (map[string]bool, error)

func noneRecorded(context.Context) (map[string]bool, error) { return nil, nil }

func sweepRepository(ctx context.Context, api ECRAPI, target provider.RegistryTarget, repository string, kept map[string]bool, recorded Recorded, pushedBefore time.Time) ([]string, error) {
	var unkept []string
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
					unkept = append(unkept, tag)
				}
			}
		}
		if token = listed.NextToken; token == nil {
			break
		}
	}
	return deleteTags(ctx, api, target, repository, unkept, recorded)
}

func removeImages(ctx context.Context, api ECRAPI, target provider.RegistryTarget, imageRefs []string, kept map[string]bool, recorded Recorded) ([]string, error) {
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
		gone, err := deleteTags(ctx, api, target, repository, tagged[repository], recorded)
		removed = append(removed, gone...)
		errs = append(errs, err)
	}
	return removed, errors.Join(errs...)
}

func deleteTags(ctx context.Context, api ECRAPI, target provider.RegistryTarget, repository string, tags []string, recorded Recorded) ([]string, error) {
	var removed []string
	for batch := range slices.Chunk(tags, batchDeleteImages) {
		kept, err := recorded(ctx)
		if err != nil {
			return removed, fmt.Errorf("read the images the project's stacks record again before removing any from %s: %w", repository, err)
		}
		ids := make([]ecrtypes.ImageIdentifier, 0, len(batch))
		for _, tag := range batch {
			if !kept[target.Server+"/"+repository+":"+tag] {
				ids = append(ids, ecrtypes.ImageIdentifier{ImageTag: aws.String(tag)})
			}
		}
		if len(ids) == 0 {
			continue
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

func Reconcile(ctx context.Context, api ECRAPI, imageRef string, recorded Recorded, pushedBefore time.Time) ([]string, error) {
	target, err := Resolve(ctx, api)
	if err != nil {
		return nil, err
	}
	if !IsInRegistry(target, imageRef) {
		return nil, nil
	}
	repository, err := repositoryOf(target, imageRef)
	if err != nil {
		return nil, err
	}
	keptWithOwn := func(ctx context.Context) (map[string]bool, error) {
		kept, err := recorded(ctx)
		if err != nil {
			return nil, err
		}
		kept = maps.Clone(kept)
		if kept == nil {
			kept = map[string]bool{}
		}
		kept[imageRef] = true
		return kept, nil
	}
	kept, err := keptWithOwn(ctx)
	if err != nil {
		return nil, err
	}
	return sweepRepository(ctx, api, target, repository, kept, keptWithOwn, pushedBefore)
}

func Forget(ctx context.Context, api ECRAPI, target provider.RegistryTarget, imageRefs []string, recorded Recorded, sweptBefore time.Time) ([]string, error) {
	var ours []string
	for _, imageRef := range imageRefs {
		if IsInRegistry(target, imageRef) {
			ours = append(ours, imageRef)
		}
	}
	kept, err := recorded(ctx)
	if err != nil {
		return nil, err
	}
	removed, err := removeImages(ctx, api, target, ours, kept, recorded)
	errs := []error{err}
	for _, repository := range unkeptRepositories(target, ours, kept) {
		swept, err := sweepRepository(ctx, api, target, repository, kept, recorded, sweptBefore)
		removed = append(removed, swept...)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, deleteEmptyRepository(ctx, api, repository))
	}
	return removed, errors.Join(errs...)
}

func unkeptRepositories(target provider.RegistryTarget, imageRefs []string, kept map[string]bool) []string {
	pulled := map[string]bool{}
	for image := range kept {
		if repository, err := repositoryOf(target, image); err == nil {
			pulled[repository] = true
		}
	}
	var unkept []string
	for _, imageRef := range imageRefs {
		repository, err := repositoryOf(target, imageRef)
		if err == nil && !pulled[repository] && !slices.Contains(unkept, repository) {
			unkept = append(unkept, repository)
		}
	}
	return unkept
}

func deleteEmptyRepository(ctx context.Context, api ECRAPI, repository string) error {
	_, err := api.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(repository)})
	var occupied *ecrtypes.RepositoryNotEmptyException
	var gone *ecrtypes.RepositoryNotFoundException
	if err == nil || errors.As(err, &occupied) || errors.As(err, &gone) {
		return nil
	}
	return fmt.Errorf("delete the image repository %s, which no stack of the project pulls from any more: %w", repository, err)
}
