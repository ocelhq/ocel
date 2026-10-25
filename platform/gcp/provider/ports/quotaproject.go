package ports

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

const (
	QuotaProjectHeader  = "X-Goog-User-Project"
	quotaProjectEnvVar  = "GOOGLE_CLOUD_QUOTA_PROJECT"
	userCredential      = "authorized_user"
	federatedCredential = "external_account_authorized_user"
)

func QuotaProject(project string) []option.ClientOption {
	if project == "" || !IsBilledElsewhere() {
		return nil
	}
	return []option.ClientOption{option.WithQuotaProject(project)}
}

func QuotaProjectOf(resource string) string {
	named, scoped := strings.CutPrefix(strings.TrimPrefix(resource, "/"), "projects/")
	if !scoped || !IsBilledElsewhere() {
		return ""
	}
	project, _, _ := strings.Cut(named, "/")
	return project
}

func IsBilledElsewhere() bool {
	if os.Getenv(quotaProjectEnvVar) != "" {
		return true
	}
	found, err := google.FindDefaultCredentials(context.Background(), CloudPlatformScope)
	if err != nil || len(found.JSON) == 0 {
		return false
	}
	var named struct {
		Type         string `json:"type"`
		QuotaProject string `json:"quota_project_id"`
	}
	if err := json.Unmarshal(found.JSON, &named); err != nil {
		return false
	}
	return named.QuotaProject != "" || named.Type == userCredential || named.Type == federatedCredential
}
