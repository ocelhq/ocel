package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

var (
	SetUpPreviewProject = setUpPreviewProject
	CoverPreview        = coverPreview
	PreviewDependencies = previewDependencies
	SentDeploy          = sentDeploy
	AssertEnvironment   = assertEnvironment
)

func PreviewUpNamed(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, name string) {
	t.Helper()
	previewUp(t, fixture, dependencies, previewUpOptions{name: name})
}
