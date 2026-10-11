package gateplugin

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

const (
	title      = "ocel-preview-gate 1"
	hostLine   = "h"
	gateLine   = "g"
	emptyField = "-"
	hexDigits  = "0123456789ABCDEF"
)

type Config struct {
	hosts map[string]string
	gates map[string]previewgate.Gate
}

func EncodeConfig(hosts map[string]string, gates map[string]previewgate.Gate) string {
	var text strings.Builder
	text.WriteString(title + "\n")
	for _, slug := range slices.Sorted(maps.Keys(gates)) {
		gate := gates[slug]
		text.WriteString(strings.Join([]string{
			gateLine, escapeField(slug), escapeField(gate.Key), escapeField(gate.PasswordMAC),
			escapeList(gate.BypassSecrets), escapeList(gate.AllowOptions),
		}, " ") + "\n")
	}
	for _, host := range slices.Sorted(maps.Keys(hosts)) {
		text.WriteString(strings.Join([]string{hostLine, escapeField(strings.ToLower(host)), escapeField(hosts[host])}, " ") + "\n")
	}
	return text.String()
}

func ParseConfig(text string) (Config, error) {
	lines := strings.Split(text, "\n")
	if lines[0] != title {
		return Config{}, errors.New("the plugin config does not start with " + title)
	}
	config := Config{hosts: map[string]string{}, gates: map[string]previewgate.Gate{}}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		fields := strings.Split(line, " ")
		switch {
		case fields[0] == gateLine && len(fields) == 6:
			gate, slug, err := parseGate(fields[1:])
			if err != nil {
				return Config{}, err
			}
			config.gates[slug] = gate
		case fields[0] == hostLine && len(fields) == 3:
			host, err := unescapeField(fields[1])
			if err != nil {
				return Config{}, err
			}
			slug, err := unescapeField(fields[2])
			if err != nil {
				return Config{}, err
			}
			config.hosts[host] = slug
		default:
			return Config{}, errors.New("the plugin config holds a line that is no gate and no host: " + line)
		}
	}
	for host, slug := range config.hosts {
		if _, gated := config.gates[slug]; !gated {
			return Config{}, errors.New("the plugin config routes " + host + " to " + slug + ", which has no gate")
		}
	}
	return config, nil
}

func (c Config) ListHosts() []string { return slices.Sorted(maps.Keys(c.hosts)) }

func (c Config) FindGate(host string) (previewgate.Gate, bool) {
	host = strings.ToLower(host)
	if name, _, found := strings.Cut(host, ":"); found {
		host = name
	}
	gate, gated := c.gates[c.hosts[host]]
	return gate, gated && c.hosts[host] != ""
}

func parseGate(fields []string) (previewgate.Gate, string, error) {
	slug, err := unescapeField(fields[0])
	if err != nil {
		return previewgate.Gate{}, "", err
	}
	key, err := unescapeField(fields[1])
	if err != nil {
		return previewgate.Gate{}, "", err
	}
	mac, err := unescapeField(fields[2])
	if err != nil {
		return previewgate.Gate{}, "", err
	}
	bypass, err := unescapeList(fields[3])
	if err != nil {
		return previewgate.Gate{}, "", err
	}
	options, err := unescapeList(fields[4])
	if err != nil {
		return previewgate.Gate{}, "", err
	}
	return previewgate.Gate{Key: key, PasswordMAC: mac, BypassSecrets: bypass, AllowOptions: options}, slug, nil
}

func escapeList(values []string) string {
	if len(values) == 0 {
		return emptyField
	}
	escaped := make([]string, len(values))
	for i, value := range values {
		escaped[i] = escapeField(value)
	}
	return strings.Join(escaped, ",")
}

func unescapeList(field string) ([]string, error) {
	if field == emptyField {
		return nil, nil
	}
	parts := strings.Split(field, ",")
	values := make([]string, len(parts))
	for i, part := range parts {
		value, err := unescapeField(part)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func escapeField(value string) string {
	if value == "" {
		return emptyField
	}
	var escaped strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if isPlain(b) && (len(value) > 1 || b != '-') {
			escaped.WriteByte(b)
			continue
		}
		escaped.WriteByte('%')
		escaped.WriteByte(hexDigits[b>>4])
		escaped.WriteByte(hexDigits[b&0xf])
	}
	return escaped.String()
}

func unescapeField(field string) (string, error) {
	if field == emptyField {
		return "", nil
	}
	var value strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] != '%' {
			value.WriteByte(field[i])
			continue
		}
		if i+2 >= len(field) {
			return "", errors.New("the plugin config holds a truncated escape in " + field)
		}
		high, low := strings.IndexByte(hexDigits, field[i+1]), strings.IndexByte(hexDigits, field[i+2])
		if high < 0 || low < 0 {
			return "", errors.New("the plugin config holds a bad escape in " + field)
		}
		value.WriteByte(byte(high<<4 | low))
		i += 2
	}
	return value.String(), nil
}

func isPlain(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~' || b == '/'
}
