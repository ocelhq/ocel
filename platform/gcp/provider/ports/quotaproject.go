package ports

import (
	"context"
	"encoding/json"
	"os"
)

const (
	QuotaProjectEnvVar  = "GOOGLE_CLOUD_QUOTA_PROJECT"
	userCredential      = "authorized_user"
	federatedCredential = "external_account_authorized_user"
)

func findQuotaProject(project string, credentials Credentials) string {
	if named := os.Getenv(QuotaProjectEnvVar); named != "" {
		return named
	}
	if isBilledElsewhere(credentials) {
		return project
	}
	return ""
}

func isBilledElsewhere(credentials Credentials) bool {
	found, err := credentials.Find(context.Background())
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

func (c *Clients) QuotaProject() string {
	project, _ := c.quotaProject.get(func() (string, error) {
		if c.Emulated() {
			return "", nil
		}
		return findQuotaProject(c.Project, c.Credentials), nil
	})
	return project
}
