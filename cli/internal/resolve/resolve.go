package resolve

import (
	"context"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
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
	Type linksv1.LinkType
	Env  map[string]string
}

func StubAccount(_ context.Context, apiURL, token, projectID string) (Account, error) {
	return Account{
		OrgID:     "org_stub",
		ProjectID: projectID,
		UserID:    "user_stub",
		EnvVars:   map[string]string{},
		APIURL:    apiURL,
		Token:     token,
	}, nil
}

func StubLiveValues(_ context.Context, apiURL, token, projectID string, keys []string) (map[string]string, error) {
	return make(map[string]string, len(keys)), nil
}
