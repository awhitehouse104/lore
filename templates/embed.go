package templates

import "embed"

// Knowledge contains the immutable initialization baselines. Existing files
// are never overwritten by init.
//
//go:embed all:knowledge
var Knowledge embed.FS
