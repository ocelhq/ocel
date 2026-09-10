package resolve

import (
	"context"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Account struct {
	OrgID     string
	ProjectID string
	UserID    string
	EnvVars   map[string]string
	APIURL    string
	Token     string
}

type Resource struct {
	Name string
	Type resourcesv1.ResourceType
	Env  map[string]string
}

func StubLiveValues(_ context.Context, apiURL, token, projectID string, keys []string) (map[string]string, error) {
	return make(map[string]string, len(keys)), nil
}
