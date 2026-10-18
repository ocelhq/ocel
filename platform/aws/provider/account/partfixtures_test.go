package account

import (
	"bytes"
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func namesFor(tier environment.Tier, kind edge.Kind) edgeNames {
	names, err := edgeNamesFor(defaultNamespace, tier, kind)
	if err != nil {
		panic("no edge parameter names for tier " + string(tier) + " and kind " + string(kind))
	}
	return names
}

func cloudflareNames(tier environment.Tier) edgeNames { return namesFor(tier, KindCloudflare) }

func fixtureRefs() stackRefs {
	return stackRefs{
		assetBucket:         "ocel-assets",
		assetBucketARN:      "arn:aws:s3:::ocel-assets",
		stateTable:          "ocel-state",
		stateTableARN:       "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state",
		stateTableStreamARN: "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state/stream/2026-01-01T00:00:00.000",
	}
}

func everyPart() PartSet {
	set := PartSet{}
	for _, name := range partNames() {
		set[name] = true
	}
	return set
}

func declaredResources(body string) []templateResource {
	var tmpl struct {
		Resources json.RawMessage `json:"Resources"`
	}
	if err := json.Unmarshal([]byte(body), &tmpl); err != nil || len(tmpl.Resources) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(tmpl.Resources))
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	var out []templateResource
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return out
		}
		var resource struct {
			Type string `json:"Type"`
		}
		if err := decoder.Decode(&resource); err != nil {
			return out
		}
		out = append(out, templateResource{id: key.(string), kind: resource.Type})
	}
	return out
}

func coreBody(ns Namespace, tier environment.Tier, broughtKey string) string {
	body, err := coreStackTemplate(ns, tier, broughtKey).Body()
	if err != nil {
		panic(err)
	}
	return body
}

func partTemplate(name string, tier environment.Tier) string {
	return partTemplateWith(name, tier, everyPart())
}

func partTemplateWith(name string, tier environment.Tier, alongside PartSet) string {
	return partStackFor(name, tier, alongside).body
}

func partStackFor(name string, tier environment.Tier, alongside PartSet) partStack {
	f, ok := partNamed(name)
	if !ok {
		panic("no part named " + name)
	}
	return renderedTemplate(f, partInputs{
		ns:        defaultNamespace,
		tier:      tier,
		refs:      fixtureRefs(),
		alongside: alongside,
	})
}

func renderedTemplate(f part, in partInputs) partStack {
	stack, err := f.template(in)
	if err != nil {
		panic(err)
	}
	return stack
}
