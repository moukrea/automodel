// Package automodel embeds the files the binary ships with: the default
// model catalog and the routing eval cases.
package automodel

import _ "embed"

// Catalog is the catalog.toml of this release, written to the config
// directory at install and kept up to date while the user hasn't edited it.
//
//go:embed catalog.toml
var Catalog []byte

// EvalCases is testdata/eval/routing.jsonl, used by `automodel eval`.
//
//go:embed testdata/eval/routing.jsonl
var EvalCases []byte
