package dashboard

import "embed"

// Assets contains the embedded dashboard single-page application.
//
//go:embed index.html
var Assets embed.FS
