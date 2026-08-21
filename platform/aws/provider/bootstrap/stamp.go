package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

const (
	TagSchema         = "ocel:schema"
	TagDigest         = "ocel:digest"
	TagBootstrappedBy = "ocel:bootstrapped-by"
	TagAutoHeal       = "ocel:auto-heal"
)

const devWriter = "dev"

type Stamp struct {
	Schema    int
	Digest    string
	WrittenBy string
	AutoHeal  bool
}

type StackStamp struct {
	Name      string
	Feature   string
	Present   bool
	Schema    int
	Digest    string
	Intended  string
	WrittenBy string
}

func (s StackStamp) Current() bool {
	return s.Digest != "" && s.Digest == s.Intended
}

func (d Deployed) Stale(required []string) []StackStamp {
	var out []StackStamp
	for _, s := range d.Stacks {
		if !s.Present || s.Current() {
			continue
		}
		if s.Feature != "" && !slices.Contains(required, s.Feature) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func TemplateDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func stampTags(s Stamp) []cfntypes.Tag {
	return []cfntypes.Tag{
		{Key: aws.String(TagSchema), Value: aws.String(strconv.Itoa(s.Schema))},
		{Key: aws.String(TagDigest), Value: aws.String(s.Digest)},
		{Key: aws.String(TagBootstrappedBy), Value: aws.String(s.WrittenBy)},
		{Key: aws.String(TagAutoHeal), Value: aws.String(strconv.FormatBool(s.AutoHeal))},
	}
}

func readStamp(tags []cfntypes.Tag) Stamp {
	var s Stamp
	for _, tag := range tags {
		value := aws.ToString(tag.Value)
		switch aws.ToString(tag.Key) {
		case TagSchema:
			s.Schema, _ = strconv.Atoi(value)
		case TagDigest:
			s.Digest = value
		case TagBootstrappedBy:
			s.WrittenBy = value
		case TagAutoHeal:
			s.AutoHeal, _ = strconv.ParseBool(value)
		}
	}
	return s
}

type Writer string

func WriterFor(version string) Writer {
	return writerFor(version, vcsRevision())
}

func writerFor(version, revision string) Writer {
	w := Writer(strings.TrimSpace(version))
	if w.Release() {
		return w
	}
	if revision == "" {
		return devWriter
	}
	return Writer(devWriter + "+" + revision)
}

func (w Writer) Release() bool {
	_, ok := w.release()
	return ok
}

func (w Writer) Newer(than Writer) bool {
	mine, ok := w.release()
	if !ok {
		return false
	}
	theirs, ok := than.release()
	if !ok {
		return false
	}
	return slices.Compare(mine.core[:], theirs.core[:]) > 0 ||
		(mine.core == theirs.core && mine.pre == "" && theirs.pre != "")
}

type releaseVersion struct {
	core [3]uint64
	pre  string
}

func (w Writer) release() (releaseVersion, bool) {
	core, _, _ := strings.Cut(string(w), "+")
	core, pre, _ := strings.Cut(core, "-")
	parts := strings.Split(strings.TrimPrefix(core, "v"), ".")
	if len(parts) != 3 {
		return releaseVersion{}, false
	}
	var out releaseVersion
	out.pre = pre
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return releaseVersion{}, false
		}
		out.core[i] = n
	}
	return out, true
}

func (w Writer) String() string {
	if w == "" {
		return "unknown"
	}
	return string(w)
}

func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
