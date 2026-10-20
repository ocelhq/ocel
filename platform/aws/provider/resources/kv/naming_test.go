package kv

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func mustGroupID(t *testing.T, project, logicalName string) string {
	t.Helper()
	id, err := groupID("ocel", component.ResourceCoordinate(project, "prod", logicalName, naming.KindKV))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLongSlugsStayDistinct(t *testing.T) {
	const shared = "my-very-long-project-name-that-runs-past-the-old-limit"

	a, b := shared+"-alpha", shared+"-beta"

	if one, two := naming.Sanitize(a), naming.Sanitize(b); one == two {
		t.Fatalf("two slugs collapsed to the index project %q — teardown scopes on this value and would plan across projects", one)
	}
	if one, two := naming.StateBackendURL("s3", "state", a), naming.StateBackendURL("s3", "state", b); one == two {
		t.Fatalf("two slugs share the state subpath %q", one)
	}
	if one, two := mustGroupID(t, naming.Sanitize(a), "kv--cache"), mustGroupID(t, naming.Sanitize(b), "kv--cache"); one == two {
		t.Fatalf("two slugs' stores share the replication group %q", one)
	}
}

func TestAStoreIsNamedUnderTheAppFenceWithinTheReplicationGroupIDElastiCacheAccepts(t *testing.T) {
	t.Parallel()

	if got := mustGroupID(t, "shop", "kv--cache"); got != testGroup {
		t.Errorf("groupID = %q, want %q", got, testGroup)
	}

	long := mustGroupID(t, strings.Repeat("p", 30), "kv--"+strings.Repeat("c", 10))
	if len(long) > maxReplicationGroupIDLen {
		t.Errorf("groupID = %q, %d characters, want <= %d", long, len(long), maxReplicationGroupIDLen)
	}
	if strings.Contains(long, "--") || strings.HasSuffix(long, "-") {
		t.Errorf("groupID = %q, want no doubled or trailing hyphen ElastiCache refuses", long)
	}
}

func TestAStoreWhoseNameCannotFitIsRefused(t *testing.T) {
	t.Parallel()

	_, err := groupID("ocel", component.ResourceCoordinate("shop", "prod", "kv--"+strings.Repeat("c", 30), naming.KindKV))
	if err == nil || !strings.Contains(err.Error(), "40") {
		t.Errorf("groupID = %v, want a refusal naming ElastiCache's 40 characters", err)
	}
}
