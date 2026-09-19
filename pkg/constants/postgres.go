package constants

import (
	"maps"
	"slices"
)

const DefaultPostgresVersion = "17"

var postgresImages = map[string]string{
	"14": "postgres:14.19@sha256:962ffbe9f6418387643411b127c1db27465e5a23b9a8849bfaf45fa6323963ce",
	"15": "postgres:15.14@sha256:822f8795764a670160640888508b2a68ea5c4b045012c2de17e1d0447bdbdc99",
	"16": "postgres:16.10@sha256:21f6013073bc6b92830a2129570e2f5ec42a6c734b5a985a41e83aa58f54c3c1",
	"17": "postgres:17.6@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929",
}

func PostgresImage(version string) (string, bool) {
	image, pinned := postgresImages[version]
	return image, pinned
}

func PostgresVersions() []string {
	return slices.Sorted(maps.Keys(postgresImages))
}
