package release_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
)

func TestAnAppContainerIsNamedByProjectEnvAppWorkerAndItsRelease(t *testing.T) {
	token, err := naming.ParseReleaseToken("r1a2b3c4d")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stack  naming.StackName
		worker string
		want   string
	}{
		{naming.AppStack("prod", "web", token), "", "acme-prod-web-r1a2b3c4d"},
		{naming.AppStack("prod", "web", token), "mailer", "acme-prod-web-mailer-r1a2b3c4d"},
		{naming.ContainerWorkersStack("prod", "web"), "mailer", "acme-prod-web-mailer-7bd99f"},
	} {
		got, err := release.ContainerName("acme", tc.stack, tc.worker, "registry.example/web:1")
		if err != nil {
			t.Errorf("%s %q: %v", tc.stack, tc.worker, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ContainerName(%s, %q) = %q, want %q", tc.stack, tc.worker, got, tc.want)
		}
	}
}
