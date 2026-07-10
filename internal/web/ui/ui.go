// Package ui embeds the built SolidJS SPA bundle so the panel ships as a single
// binary. The dist/ directory is produced by `make ui-build` (Bun + Vite); a
// committed placeholder index.html keeps `go build` working without the JS
// toolchain present.
package ui

import "embed"

// Dist is the embedded SPA bundle (the contents of dist/, including index.html
// and hashed assets). Serve it via fs.Sub(Dist, "dist").
//
//go:embed all:dist
var Dist embed.FS
