// Package web embeds the HTML templates and static files.
package web

import "embed"

//go:embed templates static
var FS embed.FS
