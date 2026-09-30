// Package web holds the browser UI. The files are embedded into panel.exe;
// the tests and package.json next to them are for Node and not embedded.
package web

import "embed"

//go:embed index.html favicon.svg css js
var Files embed.FS
