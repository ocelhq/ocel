package bootstrap

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type loggedTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			LogGroupName    string `yaml:"LogGroupName"`
			RetentionInDays int    `yaml:"RetentionInDays"`
			LoggingConfig   struct {
				LogGroup string `yaml:"LogGroup"`
			} `yaml:"LoggingConfig"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func TestEveryBootstrapFunctionLogsToAGroupItsStackOwns(t *testing.T) {
	functions := 0
	for name, body := range everyRenderedTemplate() {
		t.Run(name, func(t *testing.T) {
			var tmpl loggedTemplate
			if err := yaml.Unmarshal([]byte(body), &tmpl); err != nil {
				t.Fatalf("template is not valid YAML: %v", err)
			}
			for id, resource := range tmpl.Resources {
				if resource.Type != "AWS::Lambda::Function" {
					continue
				}
				functions++
				groupID := resource.Properties.LoggingConfig.LogGroup
				if groupID == "" {
					t.Errorf("%s names no log group, so Lambda mints /aws/lambda/<function> on first invoke with no retention and no owner to reclaim it at teardown", id)
					continue
				}
				if !strings.Contains(body, "LogGroup: !Ref "+groupID) {
					t.Errorf("%s logs to %q rather than a !Ref of a group this stack declares, so the group need not exist before the function runs", id, groupID)
				}
				group, declared := tmpl.Resources[groupID]
				if !declared || group.Type != "AWS::Logs::LogGroup" {
					t.Errorf("%s logs to %q, which is not an AWS::Logs::LogGroup in this stack, so teardown leaves it behind", id, groupID)
					continue
				}
				if group.Properties.RetentionInDays != 14 {
					t.Errorf("%s keeps logs for %d days, want 14 like every function a deploy registers", groupID, group.Properties.RetentionInDays)
				}
				if want := "/aws/lambda/${AWS::StackName}-" + id; group.Properties.LogGroupName != want {
					t.Errorf("%s is named %q, want %q: under the bootstrap credential's log-group scope and clear of the suffixed group Lambda minted for an older function", groupID, group.Properties.LogGroupName, want)
				}
			}
		})
	}
	if functions == 0 {
		t.Fatal("no rendered template declares a function, so nothing was checked")
	}
}
