package provider

import "maps"

func SecretProperties(t BindingType) []string {
	switch t {
	case BindingPostgres:
		return []string{PropertyPassword, PropertyURL}
	case BindingKV:
		return []string{PropertyPassword}
	case BindingRealtime:
		return []string{PropertySigningKey}
	}
	return nil
}

func (b Binding) WithoutSecrets() Binding {
	if len(b.Properties) == 0 {
		return b
	}
	if b.Type == BindingCustom {
		b.Properties = nil
		return b
	}
	b.Properties = maps.Clone(b.Properties)
	for _, name := range SecretProperties(b.Type) {
		delete(b.Properties, name)
	}
	return b
}
