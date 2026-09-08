package gcp

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Kind string

const (
	KindDatabase Kind = "firestore:database"
	KindBucket   Kind = "storage:bucket"
	KindKeyRing  Kind = "kms:keyring"
	KindKey      Kind = "kms:key"
	KindSecret   Kind = "secretmanager:secret"
)

const StampObject = "ocel/bootstrap.json"

const (
	stateBucketSuffix = "-state"
	passphraseSuffix  = "-pulumi-passphrase"
)

var BootstrapAPIs = []string{
	"firestore.googleapis.com",
	"storage.googleapis.com",
	"cloudkms.googleapis.com",
	"secretmanager.googleapis.com",
}

func StateBucketName(project string, class providerkit.Class) string {
	return BucketName(project, class) + stateBucketSuffix
}

func PassphraseSecret(class providerkit.Class) string {
	return bucketStem + string(class) + passphraseSuffix
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

func stackItems(project string, class providerkit.Class) []item {
	return []item{
		{
			Kind: KindDatabase, Name: recordDatabase, Shared: true, Slow: true,
			Note: "every record this project holds, and both classes keep theirs in it",
		},
		{
			Kind: KindBucket, Name: BucketName(project, class),
			Note: "the artifacts this class deploys, and the stamp saying what this bootstrap is",
		},
		{
			Kind: KindBucket, Name: StateBucketName(project, class), Versioned: true,
			Note: "the state every stack in this class writes, versioned so a bad write is recoverable",
		},
		{
			Kind: KindKeyRing, Name: KeyRing, Shared: true,
			Note: "the ring both classes' keys hang on",
		},
		{
			Kind: KindKey, Name: string(class),
			Note: "the key every value this class holds is sealed under",
		},
	}
}

func parameterItems(class providerkit.Class) []item {
	return []item{{
		Kind: KindSecret, Name: PassphraseSecret(class),
		Note: "the passphrase this class's state is encrypted under, minted here and never written over",
	}}
}

func bootstrapItems(project string, class providerkit.Class) []item {
	return slices.Concat(stackItems(project, class), parameterItems(class))
}

func digestOf(items []item) string {
	sum := sha256.New()
	for _, item := range items {
		sum.Write([]byte(item.ID() + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func siblingOf(class providerkit.Class) providerkit.Class {
	if class == providerkit.ClassProduction {
		return providerkit.ClassPreview
	}
	return providerkit.ClassProduction
}
