package tracing

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"

	"github.com/ocelhq/ocel/pkg/processenv"
)

const Redacted = "[secret]"

const envAttributePrefix = "process.env."

const pulumiSecretSignature = "4dabf18193072939515e22adb298388d"

var (
	secretName        = regexp.MustCompile(`(?i)SECRET|TOKEN|PASSWORD|PASSWD|KEY|CREDENTIAL|PRIVATE|AUTH|SESSION|ASSERTION|VERIFIER|SIGNED_?(?:JWT|BLOB)`)
	pemBlock          = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)
	pulumiSecretField = regexp.MustCompile(`"(plaintext|ciphertext|value)"(\s*:\s*)"(?:[^"\\]|\\.)*"`)
	credentialHeader  = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|x-amz-security-token|x-api-key)(\s*:\s*)[^\r\n'"]*`)
	namedValue        = regexp.MustCompile(`(^|[\s?#&;,'"(-])([A-Za-z_][A-Za-z0-9_.-]*)=([^\s&'"]+)`)
	urlFragment       = regexp.MustCompile(`(://[^\s#'"]*#)([^\s'"]*)`)
	fragmentValue     = regexp.MustCompile(`(^|&)([^&=]*=)[^&]*`)
	urlPassword       = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s/:@]+:)[^\s/@]+@`)
	bearerToken       = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	jwt               = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]*){2,4}`)
	base64Run         = regexp.MustCompile(`[A-Za-z0-9/+=]{40,}`)
	xmlOpenTag        = regexp.MustCompile(`<([A-Za-z_][A-Za-z0-9_.:-]*)(?:\s[^<>]*)?>`)
	jsonKey           = regexp.MustCompile(`(\\?)"([A-Za-z0-9_.$@-]{1,128})\\?"\s*:\s*`)
	flag              = regexp.MustCompile(`^--?([A-Za-z0-9_.-]+)$`)
	flagWithValue     = regexp.MustCompile(`^(--?([A-Za-z0-9_.-]+)=)`)
	flagThenValue     = regexp.MustCompile(`(^|\s)(--?([A-Za-z0-9_.-]+)\s+)('[^']*'|"(?:[^"\\]|\\.)*"|[^\s'"-]\S*)`)
)

var knownNotSecret = map[string]bool{
	"PATH": true, "HOME": true, "PWD": true, "USER": true, "SHELL": true, "TERM": true, "TMPDIR": true,
	"LANG": true, "LC_ALL": true, "TZ": true, "CI": true,
	"NODE_ENV": true, "NODE_OPTIONS": true,
	"GOOS": true, "GOARCH": true, "GOFLAGS": true, "CGO_ENABLED": true,
	"PORT": true, "HOST": true,
	"DOCKER_HOST": true, "DOCKER_BUILDKIT": true,
	"PYTHONUNBUFFERED": true, "VIRTUAL_ENV": true,
	"TRACEPARENT":          true,
	processenv.PhaseEnvVar: true, processenv.DevServerEnvVar: true, processenv.AppFolderEnvVar: true,
	processenv.AppURLEnvVar: true, processenv.RuntimeAddressEnvVar: true, processenv.WorkerEnvVar: true,
	processenv.LiveDirEnvVar: true, processenv.SkipChecksEnvVar: true, processenv.RouterKindEnvVar: true,
	ModeEnvVar: true,
}

func isKnownNotSecret(name string) bool {
	return knownNotSecret[name]
}

func isSecretName(name string) bool {
	return secretName.MatchString(name)
}

func isSecretParameter(name string) bool {
	lower := strings.ToLower(name)
	return isSecretName(name) || strings.Contains(lower, "signature") || lower == "sig" || lower == "code"
}

func redactHeader(name, value string) string {
	switch strings.ToLower(name) {
	case "cookie", "set-cookie":
		return Redacted
	}
	if isSecretName(name) {
		return Redacted
	}
	return RedactText(value)
}

func redactEnv(name, value string) string {
	if isSecretName(name) || !isKnownNotSecret(name) && ReadMode() != ModeFull {
		return Redacted
	}
	return RedactText(value)
}

func redactURL(raw string) string {
	return redactURLPassword(redactURLFragments(redactNamedValues(raw)))
}

func redactURLFragments(s string) string {
	return urlFragment.ReplaceAllStringFunc(s, func(match string) string {
		parts := urlFragment.FindStringSubmatch(match)
		return parts[1] + fragmentValue.ReplaceAllString(parts[2], "${1}${2}"+Redacted)
	})
}

func RedactText(s string) string {
	s = pemBlock.ReplaceAllString(s, Redacted)
	s = jwt.ReplaceAllString(s, Redacted)
	s = base64Run.ReplaceAllStringFunc(s, redactAWSSecretKey)
	s = redactJSONValues(s)
	s = redactXMLElements(s)
	if strings.Contains(s, pulumiSecretSignature) {
		s = pulumiSecretField.ReplaceAllString(s, `"$1"$2"`+Redacted+`"`)
	}
	s = credentialHeader.ReplaceAllString(s, "$1$2"+Redacted)
	s = bearerToken.ReplaceAllString(s, "$1"+Redacted)
	s = redactFlagValues(s)
	return redactURL(s)
}

func redactFlagValues(s string) string {
	return flagThenValue.ReplaceAllStringFunc(s, func(match string) string {
		parts := flagThenValue.FindStringSubmatch(match)
		if !isSecretName(parts[3]) {
			return match
		}
		return parts[1] + parts[2] + Redacted
	})
}

func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		if i > 0 && !strings.HasPrefix(arg, "-") {
			if previous := flag.FindStringSubmatch(args[i-1]); previous != nil && isSecretName(previous[1]) {
				out[i] = Redacted
				continue
			}
		}
		if withValue := flagWithValue.FindStringSubmatch(arg); withValue != nil && isSecretName(withValue[2]) {
			out[i] = withValue[1] + Redacted
			continue
		}
		out[i] = RedactText(arg)
	}
	return out
}

func redactJSONValues(s string) string {
	matches := jsonKey.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		if m[0] < last || !isSecretName(s[m[4]:m[5]]) {
			continue
		}
		quote := `"`
		if m[3] > m[2] {
			quote = `\"`
		}
		end, ok := jsonValueEnd(s, m[1], quote)
		if !ok {
			continue
		}
		out.WriteString(s[last:m[1]])
		out.WriteString(quote + Redacted + quote)
		last = end
	}
	out.WriteString(s[last:])
	return out.String()
}

func jsonValueEnd(s string, start int, quote string) (int, bool) {
	switch {
	case strings.HasPrefix(s[start:], quote):
		return jsonStringEnd(s, start+len(quote), quote), true
	case start < len(s) && (s[start] == '{' || s[start] == '['):
		return jsonContainerEnd(s, start, quote), true
	}
	end := start + strings.IndexAny(s[start:], ",}]\"\\\r\n")
	if end < start {
		end = len(s)
	}
	return end, end > start
}

func jsonStringEnd(s string, i int, quote string) int {
	for i < len(s) {
		switch {
		case quote == `"` && s[i] == '\\':
			i += 2
		case quote == `\"` && strings.HasPrefix(s[i:], `\\`):
			i += 2
			if strings.HasPrefix(s[i:], `\`) {
				i++
			}
			i++
		case strings.HasPrefix(s[i:], quote):
			return i + len(quote)
		default:
			i++
		}
	}
	return len(s)
}

func jsonContainerEnd(s string, i int, quote string) int {
	depth := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], quote) {
			i = jsonStringEnd(s, i+len(quote), quote)
			continue
		}
		switch s[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return len(s)
}

func redactAWSSecretKey(run string) string {
	if len(run) != 40 || strings.Contains(run, "=") ||
		!strings.ContainsAny(run, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
		!strings.ContainsAny(run, "abcdefghijklmnopqrstuvwxyz") ||
		!strings.ContainsAny(run, "0123456789") {
		return run
	}
	return Redacted
}

func redactXMLElements(s string) string {
	var out strings.Builder
	last := 0
	for _, m := range xmlOpenTag.FindAllStringSubmatchIndex(s, -1) {
		name := s[m[2]:m[3]]
		if m[0] < last || !isSecretName(name) {
			continue
		}
		end := len(s)
		if closing := strings.Index(s[m[1]:], "</"+name+">"); closing >= 0 {
			end = m[1] + closing
		}
		out.WriteString(s[last:m[1]])
		out.WriteString(Redacted)
		last = end
	}
	out.WriteString(s[last:])
	return out.String()
}

func redactNamedValues(s string) string {
	return namedValue.ReplaceAllStringFunc(s, func(match string) string {
		parts := namedValue.FindStringSubmatch(match)
		if !isSecretParameter(parts[2]) {
			return match
		}
		return parts[1] + parts[2] + "=" + Redacted
	})
}

func redactURLPassword(s string) string {
	return urlPassword.ReplaceAllString(s, "${1}"+Redacted+"@")
}

func redactAttribute(keyValue attribute.KeyValue) attribute.KeyValue {
	return attribute.KeyValue{Key: keyValue.Key, Value: rewriteTexts(string(keyValue.Key), keyValue.Value, redactTexts)}
}

func redactTexts(key string, texts []string) []string {
	if key == string(CommandArgsKey) {
		return redactArgs(texts)
	}
	redact := redactionFor(key)
	for i, text := range texts {
		texts[i] = redact(text)
	}
	return texts
}

func redactionFor(key string) func(string) string {
	if _, header, ok := strings.Cut(key, ".header."); ok {
		return func(v string) string { return redactHeader(header, v) }
	}
	if name, ok := strings.CutPrefix(key, envAttributePrefix); ok {
		return func(v string) string { return redactEnv(name, v) }
	}
	return RedactText
}

func rewriteTexts(key string, value attribute.Value, rewrite func(key string, texts []string) []string) attribute.Value {
	switch value.Type() {
	case attribute.STRING:
		return attribute.StringValue(rewrite(key, []string{value.AsString()})[0])
	case attribute.BYTESLICE:
		return attribute.ByteSliceValue([]byte(rewrite(key, []string{string(value.AsByteSlice())})[0]))
	case attribute.STRINGSLICE:
		return attribute.StringSliceValue(rewrite(key, value.AsStringSlice()))
	case attribute.SLICE:
		items := value.AsSlice()
		if texts, ok := stringsOf(items); ok {
			return attribute.StringSliceValue(rewrite(key, texts))
		}
		for i, item := range items {
			items[i] = rewriteTexts(key, item, rewrite)
		}
		return attribute.SliceValue(items...)
	case attribute.MAP:
		entries := value.AsMap()
		for i, entry := range entries {
			entries[i].Value = rewriteTexts(key+"."+string(entry.Key), entry.Value, rewrite)
		}
		return attribute.MapValue(entries...)
	default:
		return value
	}
}

func stringsOf(items []attribute.Value) ([]string, bool) {
	texts := make([]string, len(items))
	for i, item := range items {
		if item.Type() != attribute.STRING {
			return nil, false
		}
		texts[i] = item.AsString()
	}
	return texts, true
}

type SecretValues struct {
	mu       sync.RWMutex
	values   map[string]bool
	replacer *strings.Replacer
}

func (s *SecretValues) Add(values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	added := false
	for _, value := range values {
		if value != "" && !s.values[value] {
			if s.values == nil {
				s.values = map[string]bool{}
			}
			s.values[value], added = true, true
		}
	}
	if !added {
		return
	}
	longestFirst := slices.SortedFunc(maps.Keys(s.values), func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(longestFirst))
	for _, value := range longestFirst {
		pairs = append(pairs, value, Redacted)
	}
	s.replacer = strings.NewReplacer(pairs...)
}

func (s *SecretValues) RedactText(text string) string {
	return RedactText(s.hide(text))
}

func (s *SecretValues) RedactAttributes(attrs []attribute.KeyValue) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, len(attrs))
	for i, keyValue := range attrs {
		hidden := rewriteTexts(string(keyValue.Key), keyValue.Value, func(_ string, texts []string) []string {
			for j, text := range texts {
				texts[j] = s.hide(text)
			}
			return texts
		})
		out[i] = redactAttribute(attribute.KeyValue{Key: keyValue.Key, Value: hidden})
	}
	return out
}

func (s *SecretValues) hide(text string) string {
	s.mu.RLock()
	replacer := s.replacer
	s.mu.RUnlock()
	if replacer == nil {
		return text
	}
	return replacer.Replace(text)
}
