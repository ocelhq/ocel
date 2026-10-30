package terraform

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

const (
	zoneProperty   = "zone"
	variablePrefix = "var."
)

type expression struct {
	ConstantValue any      `json:"constant_value"`
	References    []string `json:"references"`
}

type providerConfig struct {
	Expressions map[string]expression `json:"expressions"`
}

type configModule struct {
	Resources []struct {
		Address           string         `json:"address"`
		ProviderConfigKey string         `json:"provider_config_key"`
		Expressions       map[string]any `json:"expressions"`
	} `json:"resources"`
	ModuleCalls map[string]struct {
		Module configModule `json:"module"`
	} `json:"module_calls"`
}

type configuration struct {
	ProviderConfig map[string]providerConfig `json:"provider_config"`
	RootModule     configModule              `json:"root_module"`
}

func (c configuration) providerKeys() map[string]string {
	keys := map[string]string{}
	c.eachResource(func(address string, providerKey string, _ map[string]any) {
		keys[address] = providerKey
	})
	return keys
}

func (c configuration) expressions() map[string]map[string]any {
	out := map[string]map[string]any{}
	c.eachResource(func(address string, _ string, expressions map[string]any) {
		out[address] = expressions
	})
	return out
}

func (c configuration) eachResource(visit func(address, providerKey string, expressions map[string]any)) {
	var walk func(m configModule, calls []string)
	walk = func(m configModule, calls []string) {
		for _, r := range m.Resources {
			visit(configAddress(calls, r.Address), r.ProviderConfigKey, r.Expressions)
		}
		for name, call := range m.ModuleCalls {
			walk(call.Module, append(calls[:len(calls):len(calls)], name))
		}
	}
	walk(c.RootModule, nil)
}

func configAddress(calls []string, address string) string {
	return strings.Join(append(calls[:len(calls):len(calls)], address), " ")
}

type variable struct {
	Value any `json:"value"`
}

func (c configuration) region(r resource, props properties.Properties, providers map[string]string, variables map[string]variable) (string, bool) {
	if region, known, decided := props.OwnRegion(properties.TypeVendor(r.typ)); decided {
		return region, known
	}
	key, found := providers[configAddress(moduleCalls(r.module), r.typ+"."+r.name)]
	if !found {
		return "", false
	}
	expressions := c.ProviderConfig[key].Expressions
	if held, set := expressions[properties.RegionPath]; set {
		return held.text(key, variables)
	}
	if held, set := expressions[zoneProperty]; set {
		at, known := held.text(key, variables)
		if !known {
			return "", false
		}
		return properties.ZoneRegion(at), true
	}
	return "", false
}

func (e expression) text(providerKey string, variables map[string]variable) (string, bool) {
	if text, constant := e.ConstantValue.(string); constant {
		return text, text != ""
	}
	if len(e.References) != 1 || isModuleProvider(providerKey) {
		return "", false
	}
	name, isVariable := strings.CutPrefix(e.References[0], variablePrefix)
	if !isVariable {
		return "", false
	}
	text, known := variables[name].Value.(string)
	return text, known && text != ""
}

func isModuleProvider(key string) bool {
	return strings.Contains(key, ":")
}
