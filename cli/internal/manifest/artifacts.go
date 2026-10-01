package manifest

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/containerimage"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func attachArtifact(manifestApp *contractv1.ManifestApp, a app, functions []*contractv1.ManifestFunction) error {
	if a.Compute != provider.ComputeContainer {
		manifestApp.Artifact = serverlessArtifact(functions)
		return nil
	}
	if a.Image == "" {
		return fmt.Errorf("app %q runs on container compute and names no image, so the manifest would hand a provider an app with nothing to run", a.Name)
	}
	if !containerimage.IsPinned(a.Image) {
		return fmt.Errorf("app %q names image %q, and a release pins one repository at one digest: a tag repoints under a running release, so it never rides in the identity", a.Name, a.Image)
	}
	manifestApp.Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
		Image:           a.Image,
		HealthCheckPath: a.HealthCheckPath,
		Arch:            a.Arch,
		MinInstances:    uint32(a.Instances.Min),
		MaxInstances:    uint32(a.Instances.Max),
	}}
	return nil
}

func serverlessArtifact(functions []*contractv1.ManifestFunction) *contractv1.ManifestApp_Serverless {
	return &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: functions}}
}
