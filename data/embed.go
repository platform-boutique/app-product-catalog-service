// Package data embeds the catalog seed files so the service binary carries its own data.
// go:embed cannot reach outside a package directory, hence this tiny package next to seed/.
// Only the JSON files are embedded (SOURCES.md stays out of the binary).
package data

import (
	"embed"
	"io/fs"
)

//go:embed seed/*.json
var files embed.FS

// Seed returns the seed directory (products.json, categories.json) as an fs.FS.
func Seed() fs.FS {
	sub, err := fs.Sub(files, "seed")
	if err != nil {
		panic(err) // cannot happen: "seed" is a valid, embedded path
	}
	return sub
}
