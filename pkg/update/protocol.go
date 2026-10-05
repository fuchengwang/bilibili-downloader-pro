// Package update provides the independent update protocol and a UI-free client.
// Network checks and background scheduling are explicit; it never installs or
// executes downloaded software and does not depend on the licensing SDK.
package update

import "time"

type Source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Artifact struct {
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	FileName string   `json:"file_name"`
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	Sources  []Source `json:"sources"`
}

type CheckRequest struct {
	AppID          string `form:"app_id" json:"app_id"`
	CurrentVersion string `form:"current_version" json:"current_version"`
	OS             string `form:"os" json:"os"`
	Arch           string `form:"arch" json:"arch"`
}

type Release struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes"`
	PublishedAt time.Time `json:"published_at"`
	Artifact    Artifact  `json:"artifact"`
}

type CheckResult struct {
	AppID          string   `json:"app_id"`
	CurrentVersion string   `json:"current_version"`
	HasUpdate      bool     `json:"has_update"`
	Release        *Release `json:"release,omitempty"`
}

type Response[T any] struct {
	Code    int    `json:"code"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    T      `json:"data,omitempty"`
}
