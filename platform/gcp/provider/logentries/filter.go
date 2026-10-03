package logentries

import (
	"strings"
	"time"
)

var quoting = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func quoted(s string) string { return `"` + quoting.Replace(s) + `"` }

func filter(q Query) string {
	terms := []string{
		`resource.type="cloud_run_revision"`,
		"(" + strings.Join(sourceTerms(q.Sources), " OR ") + ")",
		"timestamp>=" + quoted(q.Since.UTC().Format(time.RFC3339Nano)),
	}
	if !q.Until.IsZero() {
		terms = append(terms, "timestamp<="+quoted(q.Until.UTC().Format(time.RFC3339Nano)))
	}
	if q.Contains != "" {
		terms = append(terms, quoted(q.Contains))
	}
	terms = append(terms, `(log_id("run.googleapis.com/stdout") OR log_id("run.googleapis.com/stderr"))`)
	return strings.Join(terms, " AND ")
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
