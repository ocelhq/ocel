package skill

import (
	"embed"
	"io/fs"
)

//go:generate go run copy.go

//go:embed dist
var embedded embed.FS

func Files() fs.FS {
	tree, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return tree
}
