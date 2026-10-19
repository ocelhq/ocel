package toolchain

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nestBuild(t *testing.T, files tree, compiled ...string) NestBuild {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	source := t.TempDir()
	writeTree(t, source, files)
	script := `test "$NODE_ENV" = production || exit 3`
	for _, file := range compiled {
		script += " && mkdir -p " + filepath.Dir(file) + " && echo compiled > " + file
	}
	return NestBuild{App: "api", Source: source, Command: []string{"sh", "-c", script}}
}

func TestBuildNest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		files    tree
		compiled []string
		declared string
		want     string
	}{
		{
			name:     "serves the entry tsc compiles into the tsconfig's outDir",
			files:    tree{"tsconfig.json": `{"compilerOptions":{"outDir":"dist"}}`},
			compiled: []string{"dist/main.js"},
			want:     "dist/main.js",
		},
		{
			name:     "reads the outDir of the tsconfig nest build compiles with",
			files:    tree{"tsconfig.json": `{"compilerOptions":{"outDir":"dist"}}`, "tsconfig.build.json": `{"extends":"./tsconfig.json","compilerOptions":{"outDir":"build"}}`},
			compiled: []string{"build/main.js"},
			want:     "build/main.js",
		},
		{
			name:     "follows extends to the outDir a base tsconfig sets, through comments and trailing commas",
			files:    tree{"tsconfig.json": "{\n  // the base\n  \"extends\": \"./config/base.json\",\n}\n", "config/base.json": `{"compilerOptions":{"outDir":"../out",},}`},
			compiled: []string{"out/main.js"},
			want:     "out/main.js",
		},
		{
			name:     "falls back to dist, where nest build writes when no tsconfig names an outDir",
			files:    tree{"tsconfig.json": `{}`},
			compiled: []string{"dist/main.js"},
			want:     "dist/main.js",
		},
		{
			name:     "serves the entryFile nest-cli.json names",
			files:    tree{"tsconfig.json": `{"compilerOptions":{"outDir":"dist"}}`, "nest-cli.json": `{"sourceRoot":"app","entryFile":"server"}`},
			compiled: []string{"dist/server.js"},
			want:     "dist/server.js",
		},
		{
			name:     "serves the entry under its sourceRoot when tsc keeps the source tree in the outDir",
			files:    tree{"tsconfig.json": `{"compilerOptions":{"outDir":"dist"}}`},
			compiled: []string{"dist/src/main.js"},
			want:     "dist/src/main.js",
		},
		{
			name:     "reads the tsconfig nest-cli.json names",
			files:    tree{"tsconfig.json": `{"compilerOptions":{"outDir":"dist"}}`, "tsconfig.app.json": `{"compilerOptions":{"outDir":"lib"}}`, "nest-cli.json": `{"compilerOptions":{"tsConfigPath":"tsconfig.app.json"}}`},
			compiled: []string{"lib/main.js"},
			want:     "lib/main.js",
		},
		{
			name:     "serves the entrypoint the app declares",
			files:    tree{"tsconfig.json": `{"compilerOptions":{"outDir":"dist"}}`},
			compiled: []string{"dist/main.js", "dist/lambda.js"},
			declared: "dist/lambda.js",
			want:     "dist/lambda.js",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := nestBuild(t, tc.files, tc.compiled...)
			b.Entrypoint = tc.declared
			got, err := BuildNest(context.Background(), b)
			if err != nil {
				t.Fatalf("BuildNest: %v", err)
			}
			if want := filepath.Join(b.Source, filepath.FromSlash(tc.want)); got != want {
				t.Errorf("BuildNest = %s, want %s", got, want)
			}
		})
	}
}

func TestBuildNestRefusesABuildThatLeavesNoCompiledEntry(t *testing.T) {
	t.Parallel()

	b := nestBuild(t, tree{"tsconfig.json": `{"compilerOptions":{"outDir":"dist"}}`})
	_, err := BuildNest(context.Background(), b)
	if err == nil {
		t.Fatal("BuildNest = nil, want a refusal naming where it looked")
	}
	for _, said := range []string{"dist/main.js", "dist/src/main.js", `"entrypoint"`} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("BuildNest = %v, want %s named", err, said)
		}
	}
}

func TestBuildNestReportsAFailedBuildWithWhatItSaid(t *testing.T) {
	t.Parallel()

	b := nestBuild(t, tree{})
	b.Command = []string{"sh", "-c", "echo 'src/app.module.ts: error TS2307' >&2; exit 2"}
	_, err := BuildNest(context.Background(), b)
	if err == nil || !strings.Contains(err.Error(), "error TS2307") {
		t.Fatalf("BuildNest = %v, want the build's own output", err)
	}
}
