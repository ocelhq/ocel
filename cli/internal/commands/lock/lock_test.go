package lock

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func pinned() lockfile.Lock {
	return lockfile.Lock{
		CLI: "1.2.3",
		Providers: map[string]map[string]string{
			"aws": {"linux_amd64": "sha256:aa", "darwin_arm64": "sha256:bb"},
			"gcp": {"linux_amd64": "sha256:cc"},
		},
		Connectors: map[string]map[string]string{
			"vps": {"linux_amd64": "sha256:dd"},
		},
	}
}

func TestLockPrintsThePathOfTheLockItWrote(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	dependencies := newTestDependencies(func(context.Context, string) (lockfile.Lock, error) { return pinned(), nil })

	var stdout bytes.Buffer
	if err := runLock(context.Background(), dependencies, root, &stdout); err != nil {
		t.Fatalf("runLock err = %v", err)
	}

	if want := lockfile.Path(root) + "\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestLockAsJSONPrintsTheReleaseAndWhatItPinned(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	dependencies := newTestDependencies(func(context.Context, string) (lockfile.Lock, error) { return pinned(), nil })
	dependencies.Presentation = clitest.ResolveJSONPresentation

	var stdout bytes.Buffer
	if err := runLock(context.Background(), dependencies, root, &stdout); err != nil {
		t.Fatalf("runLock err = %v", err)
	}

	var got resultv1.LockResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if filepath.Base(got.GetPath()) != lockfile.Name || got.GetCliVersion() != "1.2.3" {
		t.Errorf("lock result = %v, want the lock's path and the release it pins", &got)
	}
	providers := got.GetProviders()
	if len(providers) != 2 || providers[0].GetName() != "aws" || providers[0].GetDigests()["darwin_arm64"] != "sha256:bb" || providers[1].GetName() != "gcp" {
		t.Errorf("providers = %v, want aws then gcp with their digests per platform", providers)
	}
	if connectors := got.GetConnectors(); len(connectors) != 1 || connectors[0].GetName() != "vps" {
		t.Errorf("connectors = %v, want the vps connector", connectors)
	}
}

func TestLockPrintsNothingWhenPinningFails(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	dependencies := newTestDependencies(func(context.Context, string) (lockfile.Lock, error) {
		return lockfile.Lock{}, errors.New("no network")
	})
	dependencies.Presentation = clitest.ResolveJSONPresentation

	var stdout bytes.Buffer
	if err := runLock(context.Background(), dependencies, root, &stdout); err == nil || stdout.Len() != 0 {
		t.Errorf("runLock err = %v, stdout = %q, want the failure and nothing printed", err, stdout.String())
	}
}
