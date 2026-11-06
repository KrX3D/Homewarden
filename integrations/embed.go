// Package integrations is both the integration catalog (index.json plus one folder
// per integration, served raw from GitHub or Gitea) and the built-in copy embedded in
// the binary so the catalog works offline.
package integrations

import "embed"

// FS holds index.json and every integration folder.
//
//go:embed index.json */manifest.json */*.js */*.svg
var FS embed.FS
