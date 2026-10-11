package bootstrap

import (
	"regexp"
	"strings"
)

type policyStatement struct {
	Effect    string         `yaml:"Effect"`
	Action    any            `yaml:"Action"`
	Resource  any            `yaml:"Resource"`
	Condition map[string]any `yaml:"Condition"`
}

func (s policyStatement) actions() []string   { return yamlStrings(s.Action) }
func (s policyStatement) resources() []string { return yamlStrings(s.Resource) }

func yamlStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, one := range t {
			if s, ok := one.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func iamResourceMatches(pattern, arn string) bool {
	parts := strings.Split(pattern, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(arn)
}

func iamResourceMatchesAny(patterns []string, arn string) bool {
	for _, pattern := range patterns {
		pattern = strings.ReplaceAll(pattern, "${"+paramAssetBucketARN+"}", "arn:aws:s3:::assets")
		if iamResourceMatches(pattern, arn) {
			return true
		}
	}
	return false
}
