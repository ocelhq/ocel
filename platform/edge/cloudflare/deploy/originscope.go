package cloudflare

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/records"
)

type OriginScope struct {
	Vendor string
	ID     string
}

func (o OriginScope) String() string { return o.Vendor + originVendorSeparator + o.ID }

const maxOriginLength = 40

func (o OriginScope) refuseInvalid() error {
	readable := o.String()
	switch {
	case o.Vendor == "" || o.ID == "":
		return fmt.Errorf("the cloudflare edge was opened for an origin that names no vendor or no id (%q), so it cannot tell its own resources in the account from another origin's", readable)
	case len(readable) > maxOriginLength || strings.ContainsFunc(readable, unicode.IsSpace):
		return fmt.Errorf("the origin %q is longer than %d characters or holds whitespace, so the comment that names it on a Cloudflare DNS record would not fit or read back", readable, maxOriginLength)
	}
	return nil
}

func parseOriginScope(readable string) (OriginScope, bool) {
	vendor, id, named := strings.Cut(readable, originVendorSeparator)
	if !named || vendor == "" || id == "" || strings.ContainsFunc(readable, unicode.IsSpace) {
		return OriginScope{}, false
	}
	return OriginScope{Vendor: vendor, ID: id}, true
}

func (o OriginScope) tag() string { return naming.HashTag(o.Vendor, o.ID) }

const (
	originVendorSeparator = ":"
	originIDSeparator     = "/"
	originTagPrefix       = "ocel-origin."
	originTagSeparator    = "."
	originTagEscape       = '_'
)

func (o OriginScope) scriptTag() string {
	return originTagPrefix + escapeOriginTagPart(o.Vendor) + originTagSeparator + strings.Join(escapeOriginIDParts(o.ID), originTagSeparator)
}

func escapeOriginIDParts(id string) []string {
	parts := strings.Split(id, originIDSeparator)
	for i, part := range parts {
		parts[i] = escapeOriginTagPart(part)
	}
	return parts
}

func escapeOriginTagPart(part string) string {
	var b strings.Builder
	for _, c := range []byte(part) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%c%02x", originTagEscape, c)
	}
	return b.String()
}

func unescapeOriginTagPart(part string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(part); i++ {
		if part[i] != originTagEscape {
			b.WriteByte(part[i])
			continue
		}
		if i+2 >= len(part) {
			return "", false
		}
		c, err := strconv.ParseUint(part[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		b.WriteByte(byte(c))
		i += 2
	}
	return b.String(), true
}

func readOriginTag(tags []string) (OriginScope, bool) {
	for _, tag := range tags {
		if origin, tagged := parseOriginTag(tag); tagged {
			return origin, true
		}
	}
	return OriginScope{}, false
}

func parseOriginTag(tag string) (OriginScope, bool) {
	encoded, tagged := strings.CutPrefix(tag, originTagPrefix)
	parts := strings.Split(encoded, originTagSeparator)
	if !tagged || len(parts) < 2 {
		return OriginScope{}, false
	}
	decoded := make([]string, 0, len(parts))
	for _, part := range parts {
		read, readable := unescapeOriginTagPart(part)
		if !readable || read == "" {
			return OriginScope{}, false
		}
		decoded = append(decoded, read)
	}
	return OriginScope{Vendor: decoded[0], ID: strings.Join(decoded[1:], originIDSeparator)}, true
}

type Origin struct {
	Scope              func(context.Context) (OriginScope, error)
	Records            records.Store
	KeepsObjects       bool
	ForgetStoreVersion func(context.Context, environment.Tier) error
}

type resolvedOrigin struct {
	Origin

	scopeMu sync.Mutex
	scope   *OriginScope

	accountsMu       sync.Mutex
	recordedAccounts map[environment.Tier]string
}

func (o *resolvedOrigin) originScope(ctx context.Context) (OriginScope, error) {
	o.scopeMu.Lock()
	defer o.scopeMu.Unlock()
	if o.scope != nil {
		return *o.scope, nil
	}
	scope, err := resolveOrigin(ctx, o.Scope)
	if err != nil {
		return OriginScope{}, err
	}
	o.scope = &scope
	return scope, nil
}

func resolveOrigin(ctx context.Context, origin func(context.Context) (OriginScope, error)) (OriginScope, error) {
	if origin == nil {
		return OriginScope{}, fmt.Errorf("the cloudflare edge was opened with no origin, so it cannot name what that origin owns in the account")
	}
	scope, err := origin(ctx)
	if err != nil {
		return OriginScope{}, fmt.Errorf("read which origin the cloudflare edge serves: %w", err)
	}
	if err := scope.refuseInvalid(); err != nil {
		return OriginScope{}, err
	}
	return scope, nil
}

func (p *cloudflare) readScriptOrigin(ctx context.Context, accountID, script string) (OriginScope, bool, error) {
	settings, err := p.scriptSettings(ctx, accountID, script)
	if err != nil || settings == nil {
		return OriginScope{}, false, err
	}
	origin, tagged := readOriginTag(settings.Tags)
	return origin, tagged, nil
}
