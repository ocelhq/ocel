package gcp

import (
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	raw "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func pulumiBookkeeping(project, stack string) []string {
	root := project + "/.pulumi/"
	return []string{
		root + "meta.yaml",
		root + "history/" + project + "/" + stack + "/" + stack + "-1792494597369481732.history.json",
		root + "history/" + project + "/" + stack + "/" + stack + "-1792494597369481732.checkpoint.json",
		root + "backups/" + project + "/" + stack + "/" + stack + ".1792494597369481732.json",
		root + "stacks/" + project + "/" + stack + ".json.bak",
	}
}

func liveCheckpoint(project, stack string) string {
	return project + "/.pulumi/stacks/" + project + "/" + stack + ".json"
}

func theTiersOwnStacks() []string {
	var objects []string
	for _, project := range []string{"ocel-alb-front-prod", "ocel-alb-gate-prod"} {
		stack := strings.TrimPrefix(project, "ocel-") + "--infra"
		objects = append(objects, liveCheckpoint(project, stack))
		objects = append(objects, pulumiBookkeeping(project, stack)...)
	}
	return objects
}

func stateBucketHolding(t *testing.T, objects ...string) (bootstrap.Engine, bootstrap.Survey, *storageServer) {
	t.Helper()
	server := &storageServer{buckets: map[string]*raw.Bucket{}, policies: map[string]*raw.Policy{}, objects: map[string][]string{}}
	served := httptest.NewServer(server.serve())
	t.Cleanup(served.Close)
	tier := environment.TierProduction
	buckets := googletest.TierBuckets(tier)
	server.holding(&raw.Bucket{Name: buckets[naming.StorageState]}, append([]string{bootstrap.StampObject}, objects...)...)
	b := bootstrap.Engine{Clients: googletest.Clients(served.URL)}
	return b, bootstrap.Survey{Tier: tier, Buckets: buckets}, server
}

func TestATiersDestroyIsNotHeldByTheTiersOwnStacksOrByWhatADestroyedProjectLeavesBehind(t *testing.T) {
	t.Parallel()
	objects := append(theTiersOwnStacks(), pulumiBookkeeping("ocel-alb-project-shop", "alb-prod-shop--infra")...)
	b, read, server := stateBucketHolding(t, objects...)

	if err := b.RefuseProjectStacks(t.Context(), read); err != nil {
		t.Fatalf("RefuseProjectStacks() = %v, want nil: the destroy tears the tier's own stacks down itself, and a destroyed project leaves only Pulumi's meta.yaml, history, backups and .bak", err)
	}
	if listed := server.objectListings(); listed != 1 {
		t.Errorf("RefuseProjectStacks() listed the state bucket %d times, want once", listed)
	}
}

func TestATiersDestroyIsRefusedNamingAProjectStackStillLiveInItsStateBucket(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]string{
		"plain":      "ocel-alb-project-shop/.pulumi/stacks/ocel-alb-project-shop/alb-prod-shop--infra.json",
		"compressed": "ocel-alb-project-shop/.pulumi/stacks/ocel-alb-project-shop/alb-prod-shop--infra.json.gz",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, read, server := stateBucketHolding(t, append(theTiersOwnStacks(), file)...)

			var refused refusal.Refusal
			err := b.RefuseProjectStacks(t.Context(), read)
			if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
				t.Fatalf("RefuseProjectStacks() = %v, want a %s refusal", err, refusal.CodeNotReady)
			}
			if !strings.Contains(refused.Message, "ocel-alb-project-shop/alb-prod-shop--infra") {
				t.Errorf("RefuseProjectStacks() refused with %q, want it to name the project's stack", refused.Message)
			}
			if !strings.Contains(refused.Message, "`ocel projects destroy --tier production`") {
				t.Errorf("RefuseProjectStacks() refused with %q, want it to name the command that destroys the tier's projects", refused.Message)
			}
			if listed := server.objectListings(); listed != 1 {
				t.Errorf("RefuseProjectStacks() listed the state bucket %d times, want once", listed)
			}
		})
	}
}

func TestTakingTheStateBucketIsRefusedNamingAStackOfTheTiersOwnStillLiveInIt(t *testing.T) {
	t.Parallel()
	b, read, server := stateBucketHolding(t, theTiersOwnStacks()...)
	target := tierBucketOf(naming.StorageState)

	var refused refusal.Refusal
	err := target.KindOps().Take(t.Context(), b, read, target)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Take() = %v, want a %s refusal: a stack whose state is still live was never torn down, and deleting its state strands what it provisioned", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel-alb-front-prod/alb-front-prod--infra") {
		t.Errorf("Take() refused with %q, want it to name the live stack", refused.Message)
	}
	if len(server.removed) != 0 || len(server.deleted) != 0 {
		t.Errorf("Take() removed objects %v and buckets %v, want neither", server.removed, server.deleted)
	}
	if listed := server.objectListings(); listed != 1 {
		t.Errorf("Take() listed the state bucket %d times, want once", listed)
	}
}

func TestTakingTheStateBucketEmptiesItWhenOnlyPulumisBookkeepingOutlivesItsStacks(t *testing.T) {
	t.Parallel()
	b, read, server := stateBucketHolding(t, pulumiBookkeeping("ocel-alb-front-prod", "alb-front-prod--infra")...)
	target := tierBucketOf(naming.StorageState)

	if err := target.KindOps().Take(t.Context(), b, read, target); err != nil {
		t.Fatalf("Take() = %v, want the bucket emptied and deleted", err)
	}
	if want := []string{read.Buckets[naming.StorageState]}; !slices.Equal(server.deleted, want) {
		t.Errorf("Take() deleted %v, want %v", server.deleted, want)
	}
}
