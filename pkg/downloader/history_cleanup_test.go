package downloader

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveCompletedRecordsPreservesFilesAndOtherTasks(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	tmp := filepath.Join(dir, "stream.tmp")
	if err := os.WriteFile(tmp, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	m.tasks = []*DownloadTask{
		{ID: "done", Status: StatusCompleted, OutputPath: "old", VideoTmpPath: tmp},
		{ID: "changed", Status: StatusCompleted, OutputPath: "new"},
		{ID: "active", Status: StatusDownloading, OutputPath: "active"},
	}
	ids, err := m.RemoveCompletedRecords(map[string]string{"done": "old", "changed": "old", "active": "active"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "done" {
		t.Fatalf("unexpected removed records: %v", ids)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("temporary file changed: %v", err)
	}
	if len(m.GetTasks()) != 2 {
		t.Fatal("unrelated tasks were removed")
	}
}
