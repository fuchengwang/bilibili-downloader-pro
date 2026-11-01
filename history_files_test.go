package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckHistoryFilesDistinguishesMissingAndUnknown(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.mp4")
	if err := os.WriteFile(present, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.mp4")
	offline := filepath.Join(dir, "unavailable", "video.mp4")
	states := (&App{}).CheckHistoryFiles([]string{present, missing, offline, "relative.mp4", ""})
	for path, want := range map[string]string{present: "present", missing: "missing", offline: "unknown", "relative.mp4": "unknown", "": "unknown"} {
		if states[path] != want {
			t.Errorf("%q: got %q, want %q", path, states[path], want)
		}
	}
}

func TestCheckHistoryFilesEmptyAndDuplicatePaths(t *testing.T) {
	a := &App{}
	if got := a.CheckHistoryFiles(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty check returned %v", got)
	}
	missing := filepath.Join(t.TempDir(), "missing.mp4")
	got := a.CheckHistoryFiles([]string{missing, missing, "", ""})
	if len(got) != 2 || got[missing] != "missing" || got[""] != "unknown" {
		t.Fatalf("duplicate paths changed results: %v", got)
	}
}
