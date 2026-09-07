package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	TagSchema         = "ocel:schema"
	TagDigest         = "ocel:digest"
	TagBootstrappedBy = "ocel:bootstrapped-by"
	TagNamespace      = NamespaceTagKey
)

type Stamp struct {
	Schema    int
	Digest    string
	WrittenBy string
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

func stampTags(ns Namespace, s Stamp) []cfntypes.Tag {
	return []cfntypes.Tag{
		{Key: aws.String(TagNamespace), Value: aws.String(string(ns))},
		{Key: aws.String(TagSchema), Value: aws.String(strconv.Itoa(s.Schema))},
		{Key: aws.String(TagDigest), Value: aws.String(s.Digest)},
		{Key: aws.String(TagBootstrappedBy), Value: aws.String(s.WrittenBy)},
	}
}

func onlyDevWriterMoved(have, want []cfntypes.Tag) bool {
	from := providerkit.Writer(readStamp(have).WrittenBy)
	to := providerkit.Writer(readStamp(want).WrittenBy)
	if from == to || !from.Development() || !to.Development() {
		return false
	}
	return sameStackTags(withoutTag(have, TagBootstrappedBy), withoutTag(want, TagBootstrappedBy))
}

func withoutTag(tags []cfntypes.Tag, key string) []cfntypes.Tag {
	out := make([]cfntypes.Tag, 0, len(tags))
	for _, tag := range tags {
		if aws.ToString(tag.Key) != key {
			out = append(out, tag)
		}
	}
	return out
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
		}
	}
	return s
}
