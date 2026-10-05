//go:build bindings

package main

// Wails executes main while generating bindings. It must not start an installer
// or take the live application's instance lock on the developer's machine.
const normalApplication = false
