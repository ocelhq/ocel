package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

const (
	TagSchema         = "ocel:schema"
	TagDigest         = "ocel:digest"
	TagBootstrappedBy = "ocel:bootstrapped-by"
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

func stampTags(s Stamp) []cfntypes.Tag {
	return []cfntypes.Tag{
		{Key: aws.String(TagSchema), Value: aws.String(strconv.Itoa(s.Schema))},
		{Key: aws.String(TagDigest), Value: aws.String(s.Digest)},
		{Key: aws.String(TagBootstrappedBy), Value: aws.String(s.WrittenBy)},
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
		}
	}
	return s
}
