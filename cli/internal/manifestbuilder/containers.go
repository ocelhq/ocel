package manifestbuilder

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/appbuild"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const DefaultHealthCheckPath = "/"

func attachArtifact(manifestApp *contractv1.ManifestApp, app App, compute string, functions []*contractv1.ManifestFunction) error {
	if compute != string(provider.ComputeContainer) {
		manifestApp.Artifact = serverlessArtifact(functions)
		return nil
	}
	if app.Image == "" {
		return fmt.Errorf("manifestbuilder: app %q runs on container compute and names no image, so the manifest would hand a provider an app with nothing to run", app.Name)
	}
	if !appbuild.PinnedImage(app.Image) {
		return fmt.Errorf("manifestbuilder: app %q names image %q, and a release pins one repository at one digest: a tag repoints under a running release, so it never rides in the identity", app.Name, app.Image)
	}
	if len(functions) > 0 {
		return fmt.Errorf("manifestbuilder: app %q runs on container compute and was packed into functions as well, so two things would answer the same request", app.Name)
	}
	path := app.HealthCheckPath
	if path == "" {
		path = DefaultHealthCheckPath
	}
	manifestApp.Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
		Image:           app.Image,
		HealthCheckPath: path,
		Arch:            app.Framework.Arch,
	}}
	return nil
}

func serverlessArtifact(functions []*contractv1.ManifestFunction) *contractv1.ManifestApp_Serverless {
	sorted := slices.SortedFunc(slices.Values(functions), func(a, b *contractv1.ManifestFunction) int {
		return strings.Compare(a.GetLogicalName(), b.GetLogicalName())
	})
	return &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: sorted}}
}
