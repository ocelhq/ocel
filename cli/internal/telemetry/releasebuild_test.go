package telemetry

import (
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	telemetryPackage   = "github.com/ocelhq/ocel/cli/internal/telemetry"
	writeKeySecret     = "PH_PROJECT_KEY"
	endpointSecret     = "PH_API_HOST"
	writeKeyEnv        = "TELEMETRY_WRITE_KEY"
	endpointEnv        = "TELEMETRY_ENDPOINT"
	releaseWorkflow    = ".github/workflows/binaries.yml"
	releaseEnvironment = "release"
	releaseStep        = "Release"
)

type goreleaserBuild struct {
	ID      string   `yaml:"id"`
	Ldflags []string `yaml:"ldflags"`
}

type workflowStep struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
}

var dynamicSecretsAccess = regexp.MustCompile(`(?i)toJSON\(\s*secrets\s*\)|secrets\s*\[`)

func TestTheReleaseConfigLinksTheKeyAndEndpointIntoTheCLIFromTheEnvironment(t *testing.T) {
	var config struct {
		Builds []goreleaserBuild `yaml:"builds"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, ".goreleaser.yaml")), &config); err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(config.Builds, func(b goreleaserBuild) bool {
		return b.ID == "ocel"
	})
	if index < 0 {
		t.Fatal(".goreleaser.yaml has no ocel build")
	}
	ldflags := strings.Join(config.Builds[index].Ldflags, " ")

	for variable, env := range map[string]string{"WriteKey": writeKeyEnv, "Endpoint": endpointEnv} {
		linked := regexp.MustCompile(`(^|\s)-X\s+` + regexp.QuoteMeta(telemetryPackage+"."+variable) +
			`=\{\{\s*envOrDefault\s+"` + env + `"\s+""\s*\}\}(\s|$)`)
		if !linked.MatchString(ldflags) {
			t.Errorf("ocel build ldflags %q do not set %s.%s from %s with an empty default", ldflags, telemetryPackage, variable, env)
		}
	}
}

func TestOnlyTheReleaseJobHoldsTheTelemetrySecrets(t *testing.T) {
	holders := map[string]*yaml.Node{}
	err := filepath.WalkDir(filepath.Join(repoRoot, ".github"), func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(repoRoot, file)
		name := filepath.ToSlash(relative)
		if dynamicSecretsAccess.Match(content) {
			t.Errorf("%s reads secrets dynamically, which would hand a job every secret its environment holds", name)
		}
		if path.Dir(name) != ".github/workflows" || !isYAML(name) {
			if mentionsTelemetrySecret(string(content)) {
				holders[name] = nil
			}
			return nil
		}
		for holder, node := range workflowParts(t, name, content) {
			if mentionsTelemetrySecret(scalarsOf(node)) {
				holders[holder] = node
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	want := releaseWorkflow + " job release"
	if names := slices.Sorted(maps.Keys(holders)); !slices.Equal(names, []string{want}) {
		t.Fatalf("telemetry secrets are named by %v, want only by %s", names, want)
	}

	var job struct {
		Environment yaml.Node      `yaml:"environment"`
		Steps       []workflowStep `yaml:"steps"`
	}
	if err := holders[want].Decode(&job); err != nil {
		t.Fatal(err)
	}
	if environment := environmentName(job.Environment); environment != releaseEnvironment {
		t.Errorf("%s runs in environment %q, want %q so only it resolves the secrets", want, environment, releaseEnvironment)
	}
	step := slices.IndexFunc(job.Steps, func(s workflowStep) bool {
		return s.Name == releaseStep
	})
	if step < 0 {
		t.Fatalf("%s has no %s step", want, releaseStep)
	}
	for env, secret := range map[string]string{writeKeyEnv: writeKeySecret, endpointEnv: endpointSecret} {
		wired := regexp.MustCompile(`^\$\{\{\s*secrets\.` + secret + `\s*\}\}$`)
		if got := job.Steps[step].Env[env]; !wired.MatchString(got) {
			t.Errorf("%s step env %s = %q, want secrets.%s handed to goreleaser", releaseStep, env, got, secret)
		}
	}
}

func isYAML(name string) bool {
	extension := filepath.Ext(name)
	return extension == ".yml" || extension == ".yaml"
}

func mentionsTelemetrySecret(text string) bool {
	return strings.Contains(text, writeKeySecret) || strings.Contains(text, endpointSecret)
}

func workflowParts(t *testing.T, name string, content []byte) map[string]*yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("%s is not a workflow mapping", name)
	}
	root := document.Content[0]
	outsideJobs := &yaml.Node{Kind: yaml.MappingNode}
	parts := map[string]*yaml.Node{name + " outside jobs": outsideJobs}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Value != "jobs" {
			outsideJobs.Content = append(outsideJobs.Content, key, value)
			continue
		}
		for j := 0; j+1 < len(value.Content); j += 2 {
			parts[name+" job "+value.Content[j].Value] = value.Content[j+1]
		}
	}
	return parts
}

func scalarsOf(node *yaml.Node) string {
	if node.Kind == yaml.ScalarNode {
		return node.Value
	}
	var text strings.Builder
	for _, child := range node.Content {
		text.WriteString(scalarsOf(child))
		text.WriteString("\n")
	}
	return text.String()
}

func environmentName(node yaml.Node) string {
	if node.Kind == yaml.MappingNode {
		var environment struct {
			Name string `yaml:"name"`
		}
		if err := node.Decode(&environment); err == nil {
			return environment.Name
		}
	}
	return node.Value
}
