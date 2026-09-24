// Package templates owns the scaffold assets used by cnips add.
package templates

import "embed"

//go:embed *.json
var files embed.FS

func Read(name string) ([]byte, error) { return files.ReadFile(name) }
