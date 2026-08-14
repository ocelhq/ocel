package appbuilder

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const deploymentIDEnv = "NEXT_DEPLOYMENT_ID"

const deploymentIDFileName = "deployment-id"

func mintDeploymentID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint deployment id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func deploymentIDPath(projectDir string) string {
	return filepath.Join(projectDir, scratchDirName, outputDirName, deploymentIDFileName)
}

func writeDeploymentID(projectDir, id string) error {
	path := deploymentIDPath(projectDir)
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		return fmt.Errorf("record deployment id at %s: %w", path, err)
	}
	return nil
}

func DeploymentID(projectDir string) (string, error) {
	path := deploymentIDPath(projectDir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no deployment id at %s; the build output predates this CLI, so re-run `ocel build`",
			filepath.Join(scratchDirName, outputDirName, deploymentIDFileName))
	}
	if err != nil {
		return "", fmt.Errorf("read deployment id at %s: %w", path, err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("deployment id at %s is empty; re-run `ocel build`", path)
	}
	return id, nil
}

func withDeploymentID(vars map[string]string, id string) map[string]string {
	merged := make(map[string]string, len(vars)+1)
	for key, value := range vars {
		merged[key] = value
	}
	merged[deploymentIDEnv] = id
	return merged
}
