package previewprotection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

var generatingCommands = map[string][]string{
	"rotate": {"password", "rotate"},
	"create": {"bypass", "create"},
}

func readRunLogs(t *testing.T, project clitest.FakeProject) string {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(project.Root, ".ocel", "runs", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("the run left no log to check")
	}
	var all strings.Builder
	for _, log := range logs {
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(raw)
	}
	return all.String()
}

func generatedSecret(t *testing.T, project clitest.FakeProject, command string) string {
	t.Helper()
	record := recorded(t, project)
	if command == "create" {
		return record.Gate.BypassSecrets[len(record.Gate.BypassSecrets)-1]
	}
	return record.Password
}

func TestAGeneratedSecretReachesStdoutAndNeverTheNarrationOrTheRunLog(t *testing.T) {
	for _, format := range []string{"human", "json"} {
		for command, args := range generatingCommands {
			t.Run(format+" "+command, func(t *testing.T) {
				if format == "json" {
					useJSONOutput(t)
				}
				project := protectedProject(t)

				stdout, stderr, err := execute(t, project, "", args...)

				if err != nil {
					t.Fatalf("%v: %v; stderr=%s", args, err, stderr)
				}
				secret := generatedSecret(t, project, command)
				if !strings.Contains(stdout, secret) {
					t.Fatalf("stdout = %q, want the generated secret", stdout)
				}
				if strings.Contains(stderr, secret) {
					t.Errorf("stderr carries the generated secret: %s", stderr)
				}
				if strings.Contains(readRunLogs(t, project), secret) {
					t.Error("the run log carries the generated secret")
				}
			})
		}
	}
}

func TestASecretGeneratedWhileTheEdgeRefusesIsStillPrintedAndKeptOutOfTheNarration(t *testing.T) {
	for _, format := range []string{"human", "json"} {
		for command, args := range generatingCommands {
			t.Run(format+" "+command, func(t *testing.T) {
				if format == "json" {
					useJSONOutput(t)
				}
				project := protectedProject(t)
				relayEdge(project).RefusePublication(assertedFailure("the edge is unreachable"))

				stdout, stderr, err := execute(t, project, "", args...)

				if err == nil {
					t.Fatalf("%v: err = nil, want the failed publication to exit non-zero", args)
				}
				reported := err.Error()
				if format == "json" {
					reported = stderr
				}
				if !strings.Contains(reported, "ocel preview protection sync") || !strings.Contains(reported, "the edge is unreachable") {
					t.Fatalf("%v: err = %v, stderr = %s, want the failed edge named and sync as the retry", args, err, stderr)
				}
				secret := generatedSecret(t, project, command)
				if format == "json" {
					printed, edges := decodeGeneratedResult(t, command, stdout)
					if printed != secret {
						t.Errorf("result secret = %q, want the recorded one", printed)
					}
					if len(edges) != 1 || edges[0].GetPublished() || !strings.Contains(edges[0].GetError(), "unreachable") {
						t.Errorf("edges = %v, want the relay edge unpublished, naming why", edges)
					}
				} else if strings.TrimSpace(stdout) != secret {
					t.Errorf("stdout = %q, want the generated secret alone", stdout)
				}
				if strings.Contains(stderr, secret) || strings.Contains(readRunLogs(t, project), secret) {
					t.Error("a generated secret reached the narration or the run log")
				}
				if !recorded(t, project).Pending {
					t.Error("the record is not pending after the edge refused it")
				}
			})
		}
	}
}

func decodeGeneratedResult(t *testing.T, command, stdout string) (string, []*resultv1.PreviewEdgeResult) {
	t.Helper()
	if command == "create" {
		var result resultv1.PreviewBypassSecretResult
		clitest.DecodeResultInto(t, stdout, &result)
		return result.GetSecret(), result.GetEdges()
	}
	var result resultv1.PreviewPasswordResult
	clitest.DecodeResultInto(t, stdout, &result)
	return result.GetPassword(), result.GetEdges()
}
