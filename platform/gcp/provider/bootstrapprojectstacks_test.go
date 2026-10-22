package gcp

import (
	"errors"
	"net/http/httptest"
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
	for _, project := range []string{"ocel-alb-front-prod", "ocel-alb-gate-prod", "ocel-alb-front-shielded-prod"} {
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
