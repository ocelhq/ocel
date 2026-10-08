package toolchain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
)

const buildIDLength = 16

type hashedFile struct {
	rel string
	sum string
}

func artifactHash(dir string) (string, error) {
	var files []hashedFile
	walkErr := filepath.WalkDir(dir, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, current)
		if err != nil {
			return err
		}
		sum, err := fileHash(current)
		if err != nil {
			return err
		}
		files = append(files, hashedFile{rel: filepath.ToSlash(rel), sum: sum})
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("hash %s: %w", dir, walkErr)
	}

	slices.SortFunc(files, func(a, b hashedFile) int { return strings.Compare(a.rel, b.rel) })
	digest := sha256.New()
	for _, file := range files {
		fmt.Fprintf(digest, "%s\x00%s\n", file.rel, file.sum)
	}
	return hex.EncodeToString(digest.Sum(nil))[:buildIDLength], nil
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func describeArtifact(app string, framework buildoutput.Framework, entryFile string, command, worker []string, functionDir, appDir string) error {
	if err := writeJSON(filepath.Join(functionDir, buildoutput.FunctionConfigFile), buildoutput.FunctionConfig{
		Framework: framework,
		EntryFile: entryFile,
		Command:   command,
		Worker:    worker,
		ID:        rootFunctionRouteID,
		App:       app,
	}); err != nil {
		return err
	}

	buildID, err := artifactHash(functionDir)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(appDir, buildoutput.HostingFile), buildoutput.Hosting{
		Version:          buildoutput.HostingVersion,
		Framework:        framework.Name,
		FrameworkBuildID: buildID,
		RootFunction:     rootFunctionRouteID,
		Needs:            map[edge.Need]buildoutput.NeedDetail{},
	})
}

func writeJSON(dest string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}
