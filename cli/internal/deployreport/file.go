package deployreport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const fileName = "deploy-report.json"

func Path(projectDir string) string {
	return filepath.Join(projectDir, statedir.Name, fileName)
}

func Write(projectDir string, deployment *consolev1.Deployment) error {
	doc, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(deployment)
	if err != nil {
		return fmt.Errorf("encode deploy report: %w", err)
	}
	doc = append(doc, '\n')

	path := Path(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), fileName+".*")
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(doc); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func WriteSucceeded(attempt *Attempt, apps []*consolev1.App, promotion *consolev1.Promotion, incomplete error) (*consolev1.Deployment, error) {
	deployment := attempt.Succeeded(time.Now(), apps, promotion)
	if err := errors.Join(incomplete, Write(attempt.Project.Dir, deployment)); err != nil {
		return deployment, fmt.Errorf("promotion %s is live, but its deploy report is incomplete: %w", promotion.GetId(), err)
	}
	return deployment, nil
}

func Clear(projectDir string) error {
	if err := os.Remove(Path(projectDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", Path(projectDir), err)
	}
	return nil
}
