package gcp

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Kind string

const (
	KindDatabase       Kind = "firestore:database"
	KindBucket         Kind = "storage:bucket"
	KindKeyRing        Kind = "kms:keyring"
	KindKey            Kind = "kms:key"
	KindSecret         Kind = "secretmanager:secret"
	KindRepository     Kind = "artifactregistry:repository"
	KindServiceAccount Kind = "iam:serviceaccount"
)

const StampObject = "ocel/bootstrap.json"

var BootstrapAPIs = []string{
	"firestore.googleapis.com",
	"storage.googleapis.com",
	"cloudkms.googleapis.com",
	"secretmanager.googleapis.com",
	"artifactregistry.googleapis.com",
	"iam.googleapis.com",
	"run.googleapis.com",
}

type item struct {
	Kind      Kind
	Name      string
	Note      string
	Slow      bool
	Shared    bool
	Versioned bool
}

func (i item) ID() string { return string(i.Kind) + "/" + i.Name }

func provisioned(kind Kind, emulated bool) bool { return !emulated || kind != KindRepository }

func stackItems(names Names, class edge.Class, emulated bool) []item {
	items := []item{
		{
			Kind: KindDatabase, Name: names.Database(), Shared: true, Slow: true,
			Note: "every record this project stores, and both classes keep theirs in it",
		},
		{
			Kind: KindBucket, Name: names.Bucket(class),
			Note: "the artifacts this class deploys, and the stamp saying what this bootstrap is",
		},
		{
			Kind: KindBucket, Name: names.StateBucket(class), Versioned: true,
			Note: "the state every stack in this class writes, versioned so a bad write is recoverable",
		},
		{
			Kind: KindKeyRing, Name: names.KeyRing(), Shared: true,
			Note: "the ring both classes' keys hang on",
		},
		{
			Kind: KindKey, Name: string(class),
			Note: "the key every value this class stores is sealed under",
		},
		{
			Kind: KindServiceAccount, Name: names.WorkloadAccount(class),
			Note: "the identity every app in this class runs as, and the one the deploy hands Cloud Run",
		},
		{
			Kind: KindRepository, Name: names.Repository(class),
			Note: "the images this class runs, and an untagged image lives at least a week",
		},
	}
	return slices.DeleteFunc(items, func(each item) bool { return !provisioned(each.Kind, emulated) })
}

func parameterItems(names Names, class edge.Class) []item {
	return []item{{
		Kind: KindSecret, Name: names.PassphraseSecret(class),
		Note: "the passphrase this class's state is encrypted under, minted here and never written over",
	}}
}

func bootstrapItems(names Names, class edge.Class, emulated bool) []item {
	return slices.Concat(stackItems(names, class, emulated), parameterItems(names, class))
}

func digestOf(namespace provider.Namespace, items []item) string {
	sum := sha256.New()
	sum.Write([]byte(namespace.String() + "\n"))
	for _, item := range items {
		sum.Write([]byte(item.ID() + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func siblingOf(class edge.Class) edge.Class {
	if class == edge.ClassProduction {
		return edge.ClassPreview
	}
	return edge.ClassProduction
}
