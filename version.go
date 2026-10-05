package main

import (
	_ "embed"
	"encoding/json"
)

const serviceURL = "https://47.97.111.181:8090"

//go:embed wails.json
var versionConfig []byte

// The executable, package metadata and update request share one version source.
func appVersion() string {
	var cfg struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if json.Unmarshal(versionConfig, &cfg) != nil || cfg.Info.ProductVersion == "" {
		panic("wails.json has no product version")
	}
	return cfg.Info.ProductVersion
}
