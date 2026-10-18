package cloudrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"google.golang.org/api/googleapi"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const probeTokenBytes = 32

func newProbeToken() (string, error) {
	token := make([]byte, probeTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("make a startup probe token: %w", err)
	}
	return hex.EncodeToString(token), nil
}

func readProbeToken(ctx context.Context, c *gcloud.Clients, service string) (string, error) {
	services, err := c.Run()
	if err != nil {
		return "", err
	}
	current, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*runv2.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(c.ServicePath(service)).Context(ctx).Do(call...)
	})
	switch {
	case gcloud.Absent(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read the startup probe token of Cloud Run service %s: %w", service, err)
	case current.Template == nil:
		return "", nil
	}
	for _, container := range current.Template.Containers {
		for _, variable := range container.Env {
			if variable.Name == originguard.ProbeTokenVar {
				return variable.Value, nil
			}
		}
	}
	return "", nil
}
