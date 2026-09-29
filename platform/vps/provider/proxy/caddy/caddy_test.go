package caddy_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/officialimages"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

func TestTheProxyIsPulledByTheSameDigestFromWhicheverRegistryIsNamedAtTheTime(t *testing.T) {
	t.Setenv(officialimages.MirrorEnv, "")
	fromDefault := caddy.Image()
	t.Setenv(officialimages.MirrorEnv, "mirror.gcr.io/library")
	fromMirror := caddy.Image()

	digest := strings.TrimPrefix(fromDefault, "public.ecr.aws/docker/library/")
	if !strings.HasPrefix(digest, "caddy@sha256:") {
		t.Fatalf("with %s unset, the proxy is pulled as %q, want public.ecr.aws/docker/library/caddy by its pinned digest", officialimages.MirrorEnv, fromDefault)
	}
	if want := "mirror.gcr.io/library/" + digest; fromMirror != want {
		t.Errorf("with %s=mirror.gcr.io/library, the proxy is pulled as %q, want %q", officialimages.MirrorEnv, fromMirror, want)
	}
}
