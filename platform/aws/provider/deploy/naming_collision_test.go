package deploy

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestLongSlugsStayDistinct(t *testing.T) {
	const shared = "my-very-long-project-name-that-runs-past-the-old-limit"

	a, b := shared+"-alpha", shared+"-beta"

	if one, two := naming.Sanitize(a), naming.Sanitize(b); one == two {
		t.Fatalf("two slugs collapsed to the index project %q — teardown scopes on this value and would plan across projects", one)
	}
	if one, two := naming.StateBackendURL("s3", "state", a), naming.StateBackendURL("s3", "state", b); one == two {
		t.Fatalf("two slugs share the state subpath %q", one)
	}
	one := kvGroupID(resourceCoordinate(naming.Sanitize(a), "production", "kv--cache", naming.KindKV))
	two := kvGroupID(resourceCoordinate(naming.Sanitize(b), "production", "kv--cache", naming.KindKV))
	if one == two {
		t.Fatalf("two slugs' stores share the replication group %q", one)
	}
}

func TestAStoresNamesFitTheReplicationGroupIDElastiCacheAccepts(t *testing.T) {
	t.Parallel()

	long := resourceCoordinate(strings.Repeat("p", 30), strings.Repeat("e", 30), "kv--"+strings.Repeat("c", 30), naming.KindKV)
	if got := kvGroupID(long); len(got) > maxReplicationGroupIDLen {
		t.Errorf("kvGroupID() = %q, length %d, want <= %d", got, len(got), maxReplicationGroupIDLen)
	}

	shared := strings.Repeat("sessions-", 5)
	a := kvGroupID(resourceCoordinate("shop", "prod", "kv--"+shared+"alpha", naming.KindKV))
	b := kvGroupID(resourceCoordinate("shop", "prod", "kv--"+shared+"beta", naming.KindKV))
	if a == b {
		t.Errorf("kvGroupID() collided: both %q", a)
	}

	digit := kvGroupID(resourceCoordinate("7shop", "prod", "kv--cache", naming.KindKV))
	if !strings.HasPrefix(digit, "ocel-app-") || strings.Contains(digit, "--") || strings.HasSuffix(digit, "-") {
		t.Errorf("kvGroupID() = %q, want it in the app scope, with no doubled or trailing hyphen ElastiCache refuses", digit)
	}
}
