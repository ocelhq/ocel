package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

type Kind string

const (
	KindFunction        Kind = "fn"
	KindWorker          Kind = "worker"
	KindDatabase        Kind = "db"
	KindBucket          Kind = "bucket"
	KindRole            Kind = "role"
	KindQueue           Kind = "queue"
	KindLayer           Kind = "layer"
	KindUploadCompleter Kind = "upload-completer"
)

var components = map[Kind]string{
	KindFunction:        "function",
	KindWorker:          "edge-worker",
	KindDatabase:        "database",
	KindBucket:          "bucket",
	KindRole:            "role",
	KindQueue:           "queue",
	KindLayer:           "layer",
	KindUploadCompleter: "upload-completer",
}

func (k Kind) Valid() bool {
	_, ok := components[k]
	return ok
}

func (k Kind) Component() string {
	return components[k]
}

const releasePrefix = "r"

var releasePattern = regexp.MustCompile(`^r[0-9a-f]{8}$`)

type Release struct {
	token string
}

func NewRelease(deploymentID, fingerprint string) Release {
	sum := sha256.Sum256([]byte(deploymentID + "\x00" + fingerprint))
	return Release{token: releasePrefix + hex.EncodeToString(sum[:])[:8]}
}

func ParseRelease(value string) (Release, error) {
	if !releasePattern.MatchString(value) {
		return Release{}, fmt.Errorf("release token %q must be %q followed by 8 hex digits", value, releasePrefix)
	}
	return Release{token: value}, nil
}

func (r Release) String() string { return r.token }

func (r Release) IsZero() bool { return r.token == "" }

type Coordinate struct {
	Project string
	Env     string
	App     string
	Kind    Kind
	Name    string
	Release Release
}

func (c Coordinate) Validate() error {
	for field, value := range map[string]string{"project": c.Project, "env": c.Env, "app": c.App} {
		if err := Validate(field, value); err != nil {
			return err
		}
	}
	if !c.Kind.Valid() {
		return fmt.Errorf("kind %q is not one of the deploy path's roles", c.Kind)
	}
	if c.Release.IsZero() {
		return fmt.Errorf("coordinate %s/%s/%s carries no release", c.Project, c.Env, c.App)
	}
	return nil
}

func (c Coordinate) Stack() StackName {
	return AppStack(c.Env, c.App, c.Release)
}

func (c Coordinate) PhysicalName(max int) string {
	return Fit(max, WordSeparator,
		Fixed(c.Project),
		Fixed(c.Env),
		Fixed(c.App),
		Compressible(c.Name),
		Fixed(c.Release.String()),
	)
}

func (c Coordinate) PhysicalPrefix(max int) string {
	return Fit(max-1, WordSeparator,
		Fixed(c.Project),
		Fixed(c.Env),
		Compressible(c.Name),
	) + WordSeparator
}

func (c Coordinate) SliceName() string {
	return Underscore(Join(WordSeparator, c.Project, c.Env, c.App, c.Name, c.Release.String()))
}

func ResourceID(kind Kind, name string, parts ...string) string {
	return Join(WordSeparator, append([]string{string(kind), name}, parts...)...)
}

func (c Coordinate) Description(detail string) string {
	head := strings.Join(nonEmpty([]string{c.Project, c.Env, c.App}), " / ")
	if detail == "" {
		return head + "."
	}
	return head + " - " + detail
}

type Facts struct {
	ManagedBy  string
	EnvClass   string
	BuildID    string
	Deployment string
	Promotion  string
	Route      string
	ExpiresAt  string
}

func (c Coordinate) Tags(f Facts) map[string]string {
	tags := map[string]string{
		"ocel:managed-by": f.ManagedBy,
		"ocel:project":    c.Project,
		"ocel:env":        c.Env,
		"ocel:env-class":  f.EnvClass,
		"ocel:app":        c.App,
		"ocel:release":    c.Release.String(),
		"ocel:build":      f.BuildID,
		"ocel:deployment": f.Deployment,
		"ocel:promotion":  f.Promotion,
		"ocel:component":  c.Kind.Component(),
		"ocel:route":      f.Route,
		"ocel:stack":      c.Stack().String(),
		"ocel:expires-at": f.ExpiresAt,
	}
	for key, value := range tags {
		if value == "" {
			delete(tags, key)
		}
	}
	return tags
}
