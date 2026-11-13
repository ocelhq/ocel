package terraform

import "slices"

type configured struct {
	known  bool
	body   map[string]any
	blocks []any
}

var (
	unread       = configured{}
	unconfigured = configured{known: true}
)

func configuredBody(expressions map[string]any) configured {
	if expressions == nil {
		expressions = map[string]any{}
	}
	return configured{known: true, body: expressions}
}

func (c configured) field(key string, planned any) configured {
	if !c.known {
		return unread
	}
	held, set := c.body[key]
	if !set && isBlock(planned) {
		return unread
	}
	if !set {
		return unconfigured
	}
	switch block := held.(type) {
	case map[string]any:
		if !isExpression(block) {
			return configuredBody(block)
		}
	case []any:
		return configured{known: true, blocks: block}
	}
	return unread
}

func (c configured) element(count int) configured {
	if !c.known || (c.body == nil && c.blocks == nil) {
		return c
	}
	if c.body != nil && count == 1 {
		return c
	}
	if len(c.blocks) == 1 && count == 1 {
		if body, ok := c.blocks[0].(map[string]any); ok {
			return configuredBody(body)
		}
	}
	return unread
}

func (c configured) isUnconfigured() bool {
	return c.known && c.body == nil && c.blocks == nil
}

func isBlock(planned any) bool {
	switch held := planned.(type) {
	case map[string]any:
		return true
	case []any:
		return len(held) > 0 && !slices.ContainsFunc(held, func(element any) bool {
			_, object := element.(map[string]any)
			return !object
		})
	}
	return false
}
