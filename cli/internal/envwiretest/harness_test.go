package envwiretest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const envFixture = `
import { defineEnv } from "ocel/env";
import { z } from "zod";

export const env = defineEnv({
  PUBLIC_SITE_URL: { class: "plain", client: true },
  PORT: { class: "plain", schema: z.coerce.number().min(1000) },
  LOG_LEVEL: { class: "plain", schema: z.string().default("info") },
  STRIPE_API_KEY: { class: "sensitive" },
  DB_PASSWORD: { class: "secret" },
  POSTHOG_ID: { class: "plain", folders: ["/web", "/admin"] },
});
`

func setUpFixture(t *testing.T, fixture string) string {
	t.Helper()
	requireNode(t)
	repo := repoRoot(t)
	requireSDKBuild(t, repo)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel", "env.ts"), fixture)

	modules := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, filepath.Join(repo, "packages", "ocel"), filepath.Join(modules, "ocel"))
	link(t, filepath.Join(repo, "packages", "ocel", "node_modules", "zod"), filepath.Join(modules, "zod"))

	return root
}

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is not on PATH, so there is no wire to test.\nInstall node (this repo targets node 22) and put it on PATH: %v", err)
	}
}

func requireSDKBuild(t *testing.T, repo string) {
	t.Helper()
	entry := filepath.Join(repo, "packages", "ocel", "dist", "env", "index.js")
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("the ocel SDK is not built (%s is missing), so there is no wire to test.\nBuild it with: pnpm --filter ocel build", entry)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("fixture dependency %s is missing: %v", target, err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func describeProblems(problems []*resourcesv1.VariableProblem) []string {
	out := make([]string, 0, len(problems))
	for _, problem := range problems {
		out = append(out, problem.GetKey()+"@"+problem.GetFolder()+" "+problem.GetKind().String())
	}
	return out
}
