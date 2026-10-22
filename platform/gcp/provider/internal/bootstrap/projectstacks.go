package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	pulumiStacks   = "/.pulumi/stacks/"
	checkpointGlob = "*" + pulumiStacks + "*/*.{json,json.gz,json.zst}"
)

var checkpointExtensions = []string{".json", ".json.gz", ".json.zst"}

type checkpoint struct {
	project string
	stack   string
}

func (c checkpoint) String() string { return c.project + "/" + c.stack }

func (b Engine) RefuseProjectStacks(ctx context.Context, read Survey) error {
	bucket := read.Buckets[naming.StorageState]
	live, found, err := b.findCheckpoint(ctx, bucket, alb.ListTierProjects(read.Tier))
	if err != nil || !found {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s still stores the state of %s, and removing the bucket a stack is recorded in strands what that stack provisioned.\n"+
			"Run `%s` in every project deployed here first",
		bucket, live, destroyIn(read.Tier))
}

func (b Engine) findCheckpoint(ctx context.Context, bucket string, exceptProjects []string) (checkpoint, bool, error) {
	if bucket == "" {
		return checkpoint{}, false, nil
	}
	client, err := b.Clients.Storage()
	if err != nil {
		return checkpoint{}, false, err
	}
	objects := client.Bucket(bucket).Objects(ctx, &storage.Query{MatchGlob: checkpointGlob})
	for {
		attrs, err := objects.Next()
		if errors.Is(err, iterator.Done) || errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
			return checkpoint{}, false, nil
		}
		if err != nil {
			return checkpoint{}, false, fmt.Errorf("list the stacks %s stores: %w", bucket, err)
		}
		if live, parsed := parseCheckpoint(attrs.Name); parsed && !slices.Contains(exceptProjects, live.project) {
			return live, true, nil
		}
	}
}

func parseCheckpoint(object string) (checkpoint, bool) {
	_, scoped, underStacks := strings.Cut(object, pulumiStacks)
	project, file, inProject := strings.Cut(scoped, "/")
	if !underStacks || !inProject || strings.Contains(file, "/") {
		return checkpoint{}, false
	}
	for _, extension := range checkpointExtensions {
		if stack, cut := strings.CutSuffix(file, extension); cut && stack != "" {
			return checkpoint{project: project, stack: stack}, true
		}
	}
	return checkpoint{}, false
}
