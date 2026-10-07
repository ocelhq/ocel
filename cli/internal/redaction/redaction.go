package redaction

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
)

const Placeholder = "[secret]"

type Values struct {
	forms []string
}

func NewValues(values []string) Values {
	var forms []string
	for _, value := range values {
		forms = append(forms, value)
		if encoded, err := json.Marshal(value); err == nil {
			forms = append(forms, string(encoded[1:len(encoded)-1]))
		}
		if strings.Contains(value, "\n") {
			for line := range strings.SplitSeq(value, "\n") {
				forms = append(forms, strings.TrimSpace(line))
			}
		}
	}
	forms = slices.DeleteFunc(forms, func(form string) bool { return form == "" })
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	return Values{forms: slices.Compact(forms)}
}

func (v Values) Hide(text string) string {
	for _, form := range v.forms {
		text = strings.ReplaceAll(text, form, Placeholder)
	}
	return text
}

func (v Values) Writer(w io.Writer) io.Writer {
	if len(v.forms) == 0 {
		return w
	}
	return hidingWriter{w: w, values: v}
}

func (v Values) HideError(err error) error {
	if err == nil || len(v.forms) == 0 {
		return err
	}
	return hiddenError{err: err, values: v}
}

type hidingWriter struct {
	w      io.Writer
	values Values
}

func (h hidingWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(h.w, h.values.Hide(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

type hiddenError struct {
	err    error
	values Values
}

func (e hiddenError) Error() string { return e.values.Hide(e.err.Error()) }

func (e hiddenError) Unwrap() error { return e.err }
