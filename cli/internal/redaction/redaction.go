package redaction

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const placeholder = "[secret]"

type Values struct {
	forms   []string
	longest int
	starts  [256]bool
}

func NewValues(values []string) Values {
	var forms []string
	for _, value := range values {
		if !isHideableValue(value) {
			continue
		}
		forms = append(forms, value, jsonForm(value, true), jsonForm(value, false), javaScriptForm(value))
		if strings.Contains(value, "\n") {
			for line := range strings.SplitSeq(value, "\n") {
				if line = strings.TrimSpace(line); isHideableLine(line) {
					forms = append(forms, line)
				}
			}
		}
	}
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	forms = slices.Compact(forms)
	v := Values{forms: forms}
	for _, form := range forms {
		v.longest = max(v.longest, len(form))
		v.starts[form[0]] = true
	}
	return v
}

const (
	minValueBytes = 4
	minLineBytes  = 6
)

func isHideableValue(value string) bool {
	return len(value) >= minValueBytes && strings.TrimSpace(value) != ""
}

func isHideableLine(line string) bool {
	return len(line) >= minLineBytes && strings.ContainsFunc(line, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

func jsonForm(value string, escapeHTML bool) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(escapeHTML)
	if err := encoder.Encode(value); err != nil {
		return ""
	}
	quoted := strings.TrimSuffix(encoded.String(), "\n")
	return quoted[1 : len(quoted)-1]
}

func javaScriptForm(value string) string {
	var form strings.Builder
	for len(value) > 0 {
		cut := strings.IndexAny(value, "\u2028\u2029")
		if cut < 0 {
			form.WriteString(jsonForm(value, false))
			break
		}
		_, width := utf8.DecodeRuneInString(value[cut:])
		form.WriteString(jsonForm(value[:cut], false))
		form.WriteString(value[cut : cut+width])
		value = value[cut+width:]
	}
	return form.String()
}

func (v Values) Hide(text string) string {
	if len(v.forms) == 0 {
		return text
	}
	hidden, _ := v.hide([]byte(text), len(text))
	return string(hidden)
}

func (v Values) hide(text []byte, settled int) (hidden []byte, held []byte) {
	var out bytes.Buffer
	i := 0
	for i < settled {
		if form, found := v.formAt(text[i:]); found {
			out.WriteString(placeholder)
			i += len(form)
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	i = max(i, settled)
	return out.Bytes(), text[i:]
}

func (v Values) formAt(text []byte) (string, bool) {
	if !v.starts[text[0]] {
		return "", false
	}
	for _, form := range v.forms {
		if len(form) <= len(text) && string(text[:len(form)]) == form {
			return form, true
		}
	}
	return "", false
}

func (v Values) Writer(w io.Writer) *Writer {
	return &Writer{w: w, values: v}
}

func (v Values) HideError(err error) error {
	if err == nil || len(v.forms) == 0 {
		return err
	}
	return hiddenError{err: err, values: v}
}

type Writer struct {
	w      io.Writer
	values Values

	mu      sync.Mutex
	pending []byte
}

func (h *Writer) Write(p []byte) (int, error) {
	if len(h.values.forms) == 0 {
		return h.w.Write(p)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	text := slices.Concat(h.pending, p)
	settled := max(len(text)-(h.values.longest-1), bytes.LastIndexByte(text, '\n')+1)
	hidden, held := h.values.hide(text, settled)
	h.pending = held
	if _, err := h.w.Write(hidden); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (h *Writer) Flush() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pending) == 0 {
		return nil
	}
	hidden, _ := h.values.hide(h.pending, len(h.pending))
	h.pending = nil
	_, err := h.w.Write(hidden)
	return err
}

type hiddenError struct {
	err    error
	values Values
}

func (e hiddenError) Error() string { return e.values.Hide(e.err.Error()) }

func (e hiddenError) Unwrap() error { return e.err }
