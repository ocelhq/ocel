package gcp

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/oauth2/google"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	projectVariable  = "GOOGLE_CLOUD_PROJECT"
	cloudSDKVariable = "CLOUDSDK_CORE_PROJECT"
)

const unsetProject = "(unset)"

func resolveProject(ctx context.Context, named string) (string, error) {
	for _, ambient := range []func() string{
		func() string { return named },
		func() string { return os.Getenv(projectVariable) },
		func() string { return os.Getenv(cloudSDKVariable) },
		func() string { return credentialProject(ctx) },
		func() string { return gcloudProject(ctx) },
	} {
		if project := strings.TrimSpace(ambient()); project != "" {
			return project, nil
		}
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names no Google Cloud project and nothing around this run names one either: "+
			"not the application default credentials, not %s, not %s, and not gcloud's own config.\n"+
			"Name it in the options, or run `gcloud config set project <id>`",
		"project", projectVariable, cloudSDKVariable)
}

func credentialProject(ctx context.Context) string {
	found, err := google.FindDefaultCredentials(ctx, cloudPlatformScope)
	if err != nil {
		return ""
	}
	return found.ProjectID
}

func gcloudProject(ctx context.Context) string {
	said, err := exec.CommandContext(ctx, "gcloud", "config", "get-value", "project").Output()
	if err != nil {
		return ""
	}
	project := strings.TrimSpace(string(said))
	if project == unsetProject {
		return ""
	}
	return project
}
