package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Kind string

const (
	KindFunction           Kind = "fn"
	KindWorker             Kind = "worker"
	KindDatabase           Kind = "db"
	KindBucket             Kind = "bucket"
	KindRole               Kind = "role"
	KindTopic              Kind = "topic"
	KindKV                 Kind = "kv"
	KindRealtime           Kind = "realtime"
	KindRealtimeAuthorizer Kind = "realtime-authorizer"
	KindUploadCompleter    Kind = "upload-completer"
	KindService            Kind = "svc"
)

var components = map[Kind]string{
	KindFunction:           "function",
	KindWorker:             "worker",
	KindDatabase:           "database",
	KindBucket:             "bucket",
	KindRole:               "role",
	KindTopic:              "topic",
	KindKV:                 "kv",
	KindRealtime:           "realtime",
	KindRealtimeAuthorizer: "realtime-authorizer",
	KindUploadCompleter:    "upload-completer",
	KindService:            "service",
}

func (k Kind) Component() string {
	return components[k]
}

const releasePrefix = "r"

var releasePattern = regexp.MustCompile(`^r[0-9a-f]{8}$`)

type ReleaseToken struct {
	token string
}

func NewReleaseToken(buildID, fingerprint string) ReleaseToken {
	sum := sha256.Sum256([]byte(buildID + "\x00" + fingerprint))
	return ReleaseToken{token: releasePrefix + hex.EncodeToString(sum[:])[:8]}
}

func ParseReleaseToken(value string) (ReleaseToken, error) {
	if !releasePattern.MatchString(value) {
		return ReleaseToken{}, fmt.Errorf("release token %q must be %q followed by 8 hex digits", value, releasePrefix)
	}
	return ReleaseToken{token: value}, nil
}

func (r ReleaseToken) String() string { return r.token }

func (r ReleaseToken) IsZero() bool { return r.token == "" }

type Coordinate struct {
	Project string
	Env     string
	App     string
	Kind    Kind
	Name    string
	Release ReleaseToken
}

func (c Coordinate) Stack() StackName {
	return AppStack(c.Env, c.App, c.Release)
}

func (c Coordinate) Description(detail string) string {
	head := strings.Join(nonEmpty([]string{c.Project, c.Env, c.App}), " / ")
	if detail == "" {
		return head + "."
	}
	return head + " - " + detail
}

const (
	ManagedByTagKey = "ocel:managed-by"
	EnvTierTagKey   = "ocel:env-tier"
)

func OcelTags(namespace string, tier environment.Tier) map[string]string {
	return map[string]string{ManagedByTagKey: namespace, EnvTierTagKey: string(tier)}
}

type TagValues struct {
	ManagedBy string
	EnvTier   environment.Tier
	BuildID   string
	Promotion string
	Bundle    string
	ExpiresAt string
}

func (c Coordinate) Tags(v TagValues) map[string]string {
	tags := map[string]string{
		ManagedByTagKey:   v.ManagedBy,
		"ocel:project":    c.Project,
		"ocel:env":        c.Env,
		EnvTierTagKey:     string(v.EnvTier),
		"ocel:app":        c.App,
		"ocel:release":    c.Release.String(),
		"ocel:build":      v.BuildID,
		"ocel:promotion":  v.Promotion,
		"ocel:component":  c.Kind.Component(),
		"ocel:bundle":     v.Bundle,
		"ocel:stack":      c.Stack().String(),
		"ocel:expires-at": v.ExpiresAt,
	}
	for key, value := range tags {
		if value == "" {
			delete(tags, key)
		}
	}
	return tags
}
