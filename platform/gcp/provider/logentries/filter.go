package logentries

import (
	"slices"
	"strings"
	"time"
)

var quoting = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func quoted(s string) string { return `"` + quoting.Replace(s) + `"` }

func filter(q Query) string {
	return strings.Join(slices.Concat(revisionTerms(q.Sources), timeTerms(q), outputTerms(q)), " AND ")
}

func tailFilter(q Query) string {
	return strings.Join(slices.Concat(revisionTerms(q.Sources), outputTerms(q)), " AND ")
}

func revisionTerms(sources []Source) []string {
	return []string{
		`resource.type="cloud_run_revision"`,
		"(" + strings.Join(sourceTerms(sources), " OR ") + ")",
	}
}

func timeTerms(q Query) []string {
	terms := []string{"timestamp>=" + quoted(q.Since.UTC().Format(time.RFC3339Nano))}
	if !q.Until.IsZero() {
		terms = append(terms, "timestamp<="+quoted(q.Until.UTC().Format(time.RFC3339Nano)))
	}
	return terms
}

func outputTerms(q Query) []string {
	var terms []string
	if q.Contains != "" {
		terms = append(terms, quoted(q.Contains))
	}
	return append(terms, `(log_id("run.googleapis.com/stdout") OR log_id("run.googleapis.com/stderr"))`)
}

func sourceTerms(sources []Source) []string {
	terms := make([]string, len(sources))
	for i, source := range sources {
		term := "resource.labels.service_name=" + quoted(source.Service)
		if source.Revision != "" {
			term += " AND resource.labels.revision_name=" + quoted(source.Revision)
		}
		terms[i] = "(" + term + ")"
	}
	return terms
}
