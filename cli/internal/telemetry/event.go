package telemetry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/ocelhq/ocel/cli/internal/invocation"
	"github.com/ocelhq/ocel/cli/internal/version"
)

const InstallMethodEnvVar = "OCEL_INSTALL_METHOD"

const (
	installMethodNPM    = "npm"
	installMethodBinary = "binary"
)

type Identity struct {
	InstallID     string
	CLIVersion    string
	OS            string
	Arch          string
	Agent         string
	CI            string
	InstallMethod string
	ProjectKey    string
}

func NewIdentity(installID, projectKey string) Identity {
	agent, ci := invocation.Detect(os.Getenv)
	return Identity{InstallID: installID, CLIVersion: version.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Agent: agent, CI: ci, InstallMethod: detectInstallMethod(), ProjectKey: projectKey}
}

func detectInstallMethod() string {
	if os.Getenv(InstallMethodEnvVar) == installMethodNPM {
		return installMethodNPM
	}
	return installMethodBinary
}

type Event struct {
	UUID       string         `json:"uuid"`
	Name       string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Timestamp  string         `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

type Payload interface {
	name() string
	properties() map[string]any
}

type projectPayload interface {
	projectSlug() string
}

func NewEvent(identity Identity, at time.Time, payload Payload) (Event, error) {
	properties := payload.properties()
	if project, ok := payload.(projectPayload); ok {
		properties["project_id"] = projectID(identity.ProjectKey, project.projectSlug())
	}
	return newEvent(payload.name(), identity, at, properties)
}

func projectID(key, slug string) string {
	if key == "" || slug == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(slug))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

func newEvent(name string, identity Identity, at time.Time, properties map[string]any) (Event, error) {
	if !slices.Contains(allowedEvents, name) {
		return Event{}, fmt.Errorf("telemetry event %q is not on the allowlist", name)
	}
	merged := maps.Clone(properties)
	if merged == nil {
		merged = map[string]any{}
	}
	maps.Copy(merged, map[string]any{
		"cli_version":             identity.CLIVersion,
		"os":                      identity.OS,
		"arch":                    identity.Arch,
		"agent":                   identity.Agent,
		"ci":                      identity.CI,
		"install_method":          identity.InstallMethod,
		"$process_person_profile": false,
	})
	for _, property := range slices.Sorted(maps.Keys(merged)) {
		if !slices.Contains(allowedProperties, property) {
			return Event{}, fmt.Errorf("telemetry property %q is not on the allowlist", property)
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("generate telemetry event id: %w", err)
	}
	return Event{
		UUID:       id.String(),
		Name:       name,
		DistinctID: identity.InstallID,
		Timestamp:  at.UTC().Format(time.RFC3339Nano),
		Properties: merged,
	}, nil
}
