package deploy

import (
	"fmt"

	s3 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (r *release) shipArtifacts(pctx *sdk.Context, uploads []providerkit.Upload) (map[string]sdk.Resource, error) {
	if len(uploads) == 0 {
		return nil, nil
	}
	shipped := make(map[string]sdk.Resource, len(uploads))
	for _, upload := range uploads {
		bucket, err := r.store(upload.Ref.Bucket)
		if err != nil {
			return nil, fmt.Errorf("ship %s's artifact: %w", upload.Name, err)
		}
		object, err := s3.NewBucketObjectv2(pctx, naming.ResourceID(providerkit.UploadKind, upload.Name), &s3.BucketObjectv2Args{
			Bucket:     sdk.String(bucket),
			Key:        sdk.String(upload.Ref.Key),
			Source:     sdk.NewFileAsset(upload.Path),
			SourceHash: sdk.String(upload.Digest),
		})
		if err != nil {
			return nil, fmt.Errorf("ship %s's artifact: %w", upload.Name, err)
		}
		shipped[upload.Name] = object
	}
	return shipped, nil
}
