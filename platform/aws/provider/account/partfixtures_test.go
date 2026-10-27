package account

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
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
		edgeBucketARN:       "arn:aws:s3:::ocel-prod-edge-" + fixtureBucketTag,
		bucketTag:           fixtureBucketTag,
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

const fixtureBucketTag = "k3x9q2"

const (
	fixtureIssuerHost = "console.example.com/oidc"
	fixtureAudience   = "https://console.example.com"
)

func fixtureTrust() *provider.WebIdentityTrust {
	return &provider.WebIdentityTrust{
		Issuer:         "https://" + fixtureIssuerHost,
		Audience:       fixtureAudience,
		OrganizationID: "o",
		TargetID:       "t",
		GitHub:         &provider.GitHubTrust{Repository: "acme/shop", Branch: "main"},
	}
}

func fixtureDeploy(tier environment.Tier) []iam.ManagedPolicy {
	scoped := defaultNamespace.Scope(tier)
	policies, err := iam.Split("deploy", func(i int) string { return "fixture-deploy-" + strconv.Itoa(i) }, slices.Concat(AccessCredential(scoped), RuntimeLayerCredential(scoped)))
	if err != nil {
		panic(err)
	}
	return policies
}

func fixtureBucketName(tier environment.Tier, class naming.StorageClass) string {
	return defaultNamespace.BootstrapBucketName(tier, class, fixtureBucketTag)
}

func fixtureBucketOutputs(tier environment.Tier) map[string]string {
	outputs := make(map[string]string, len(bucketOutputs))
	for output, class := range bucketOutputs {
		outputs[output] = fixtureBucketName(tier, class)
	}
	return outputs
}

func coreBody(ns Namespace, tier environment.Tier, broughtKey string) string {
	return coreBodyTagged(ns, tier, broughtKey, fixtureBucketTag)
}

func writtenCoreBody(stacks *fakeCFN, ns Namespace, tier environment.Tier, broughtKey string) string {
	core, err := ns.StackNameFor(tier)
	if err != nil {
		panic(err)
	}
	buckets := naming.Buckets{}
	stacks.mu.Lock()
	for output, class := range bucketOutputs {
		buckets[class] = stacks.outputs[core][output]
	}
	stacks.mu.Unlock()
	tag, err := ns.bootstrapBucketTag(tier, buckets)
	if err != nil {
		panic(err)
	}
	return coreBodyTagged(ns, tier, broughtKey, tag)
}

func coreBodyTagged(ns Namespace, tier environment.Tier, broughtKey, bucketTag string) string {
	body, err := coreStackTemplate(ns, tier, broughtKey, bucketTag).Body()
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
		trust:     fixtureTrust(),
		deploy:    fixtureDeploy(tier),
	})
}

func renderedTemplate(f part, in partInputs) partStack {
	stack, err := f.template(in)
	if err != nil {
		panic(err)
	}
	return stack
}
