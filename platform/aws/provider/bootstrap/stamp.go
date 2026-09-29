package bootstrap

import (
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

type Stamp struct {
	Digest    string
	WrittenBy string
}

type StackStamp struct {
	Name      string
	Feature   string
	Present   bool
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

func stampTags(ns Namespace, s Stamp) []cfntypes.Tag {
	return []cfntypes.Tag{
		{Key: aws.String(cfn.TagNamespace), Value: aws.String(string(ns))},
		{Key: aws.String(cfn.TagDigest), Value: aws.String(s.Digest)},
		{Key: aws.String(cfn.TagBootstrappedBy), Value: aws.String(s.WrittenBy)},
	}
}

func readStamp(tags []cfntypes.Tag) Stamp {
	var s Stamp
	for _, tag := range tags {
		value := aws.ToString(tag.Value)
		switch aws.ToString(tag.Key) {
		case cfn.TagDigest:
			s.Digest = value
		case cfn.TagBootstrappedBy:
			s.WrittenBy = value
		}
	}
	return s
}
