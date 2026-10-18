package account

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type policyStatement struct {
	Effect    string         `json:"Effect"`
	Action    any            `json:"Action"`
	Resource  any            `json:"Resource"`
	Condition map[string]any `json:"Condition"`
}

func (s policyStatement) actions() []string   { return templateStrings(s.Action) }
func (s policyStatement) resources() []string { return templateStrings(s.Resource) }

func templateStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, one := range t {
			if s, ok := templateString(one); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		if s, ok := templateString(t); ok {
			return []string{s}
		}
	}
	return nil
}

func templateString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case map[string]any:
		if len(t) != 1 {
			return "", false
		}
		if name, ok := t["Ref"].(string); ok {
			return name, true
		}
		if text, ok := t["Fn::Sub"].(string); ok {
			return text, true
		}
		if attribute, ok := t["Fn::GetAtt"].([]any); ok && len(attribute) == 2 {
			return fmt.Sprintf("%v.%v", attribute[0], attribute[1]), true
		}
	}
	return "", false
}

type templateText string

func (s *templateText) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	text, ok := templateString(v)
	if !ok {
		return fmt.Errorf("%s is neither a string nor a Ref, Fn::Sub or Fn::GetAtt", b)
	}
	*s = templateText(text)
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
