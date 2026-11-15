// Package starters embeds the starter templates. Every template file ends in
// .tmpl so the Go tool never treats a starter's code as part of this module.
package starters

import "embed"

// FS holds the templates, rooted at go/<area>/<name>/.
//
//go:embed all:go
var FS embed.FS
