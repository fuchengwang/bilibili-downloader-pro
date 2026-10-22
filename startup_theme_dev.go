//go:build dev

package main

import "github.com/wailsapp/wails/v2/pkg/options/assetserver"

// Development continues to use Vite's live HTML and hot reload.
func startupThemeMiddleware(func() string) assetserver.Middleware { return nil }
