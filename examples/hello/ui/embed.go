// Package ui embeds the built console plugin bundle of hello.
package ui

import "embed"

// Dist is the built bundle: plugin.json, remoteEntry.js, chunks.
//
//go:embed dist
var Dist embed.FS
