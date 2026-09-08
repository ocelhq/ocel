package gcp

import (
	"context"
	"errors"
	"net/http"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type ProjectReader interface {
	Reaches(ctx context.Context, project string) error
}

type resourceManager struct {
	endpoint string
}

func (r resourceManager) Reaches(ctx context.Context, project string) error {
	service, err := cloudresourcemanager.NewService(ctx, EmulatorREST(r.endpoint)...)
	if err != nil {
		return unauthenticated()
	}
	held, err := service.Projects.Get(project).Context(ctx).Do()
	if err != nil {
		return unreachableProject(project, err)
	}
	if held.LifecycleState == "DELETE_REQUESTED" {
		return providerkit.Refuse(providerkit.CodeDenied,
			"project %s is scheduled for deletion, and nothing this run creates in it would outlive the deploy", project)
	}
	return nil
}

func unreachableProject(project string, err error) error {
	var answered *googleapi.Error
	if !errors.As(err, &answered) {
		return providerkit.Refuse(providerkit.CodeBusy,
			"Google's project endpoint answered neither yes nor no about %s, and this credential may well be good", project)
	}
	switch answered.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return providerkit.Refuse(providerkit.CodeDenied,
			"this credential cannot read project %s: either it does not exist or nothing has granted this principal `resourcemanager.projects.get` on it",
			project)
	default:
		return providerkit.Refuse(providerkit.CodeBusy,
			"Google's project endpoint answered %d about %s, and this credential may well be good", answered.Code, project)
	}
}
