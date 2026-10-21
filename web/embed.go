// Package web embeds the HTML templates and static files.
package web

import "embed"

//go:embed all:templates all:static
var FS embed.FS
