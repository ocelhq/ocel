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

func namedProject(named string) string {
	for _, held := range []string{named, os.Getenv(projectVariable), os.Getenv(cloudSDKVariable)} {
		if project := strings.TrimSpace(held); project != "" {
			return project
		}
	}
	return ""
}

func ambientProject(ctx context.Context) (string, error) {
	for _, ambient := range []func(context.Context) string{credentialProject, gcloudProject} {
		if project := strings.TrimSpace(ambient(ctx)); project != "" {
			return project, nil
		}
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names no Google Cloud project and nothing around this run names one either: "+
			"not the application default credentials, not %s, not %s, and not gcloud's own config.\n"+
			"Name it in the options, or run `gcloud config set project <id>`",
		"project", projectVariable, cloudSDKVariable)
}

func unnamedProject() error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names no Google Cloud project and neither %s nor %s does. "+
			"A scan reads no credentials and runs no gcloud to find one, so name it in the options or in one of them",
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
