package pulumi

import "github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"

//go:generate go run ../../../../scripts/pricingbridge bridged.go

type bridgedType struct {
	token  string
	fields fieldNames
}

type fieldNames map[string]fieldName

type fieldName struct {
	terraform string
	fields    fieldNames
}

var bridgedTokens = tokenTypes()

func tokenTypes() map[string]string {
	out := make(map[string]string, len(bridged))
	for typ, held := range bridged {
		out[held.token] = typ
	}
	return out
}

func (f fieldNames) terraformName(key string) (string, fieldNames) {
	held := f[key]
	if held.terraform != "" {
		return held.terraform, held.fields
	}
	return DeriveTerraformName(key), held.fields
}

func DeriveTerraformName(pulumiKey string) string {
	return properties.Snake(pulumiKey)
}
