package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"bilibili_downloader/pkg/utils"
)

// Ends are exclusive. The file may be sparse; only Position-Start bytes have
// actually been downloaded. Records never contain cookies or signed URLs.
type byteRange struct {
	Start    int64 `json:"start"`
	Position int64 `json:"position"`
	End      int64 `json:"end"`
	active   bool
	retired  bool
	cancel   context.CancelFunc
}

type rangeRecord struct {
	Version        int         `json:"version"`
	Total          int64       `json:"total"`
	ETag           string      `json:"etag,omitempty"`
	LastModified   string      `json:"lastModified,omitempty"`
	ValidatorKey   string      `json:"validatorKey,omitempty"`   // hash of origin/path, never a signed URL
	Representation string      `json:"representation,omitempty"` // hash of the selected track's media path
	Ranges         []byteRange `json:"ranges"`
}

func rangeSidecarPath(path, suffix string) string {
	// Older records remain readable in place. New downloads keep these small
	// bookkeeping files hidden alongside the visible .downloading media.
	if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
		return path + suffix
	}
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+suffix)
}

func rangeRecordPath(path string) string { return rangeSidecarPath(path, ".ranges.json") }
func rangeMarkerPath(path string) string { return rangeSidecarPath(path, ".ranges") }

// Desktop tasks store their checkpoint in the application data directory.
// The zero value keeps the standalone stream API compatible with old caches.
type rangeStore string

func (m *DownloadManager) checkpoints() rangeStore {
	return rangeStore(filepath.Join(filepath.Dir(m.cfgMgr.GetTasksPath()), "download-checkpoints"))
}

func (s rangeStore) recordPath(path string) string {
	if s == "" {
		return rangeRecordPath(path)
	}
	return filepath.Join(string(s), fmt.Sprintf("%x.json", sha256.Sum256([]byte(storagePathKey(path)))))
}

var checkpointFileName = regexp.MustCompile(`^(?:[0-9a-f]{64}\.json|\.tmp_atomic-(?:backup-)?[0-9]+)$`)

// Only call before workers start, with a successfully loaded task list.
// Reference checks need no access to the user's download destination.
func (s rangeStore) pruneOrphans(tasks []*DownloadTask) {
	entries, err := os.ReadDir(string(s))
	if err != nil {
		return
	}
	keep := make(map[string]bool)
	for _, task := range tasks {
		for _, path := range []string{task.VideoTmpPath, task.AudioTmpPath} {
			if path != "" {
				keep[filepath.Base(s.recordPath(path))] = true
			}
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && checkpointFileName.MatchString(name) && !keep[name] {
			_ = os.Remove(filepath.Join(string(s), name))
		}
	}
}

func (s rangeStore) read(path string) (rangeRecord, error) {
	record, err := readRangeRecordAt(path, s.recordPath(path))
	if s != "" && os.IsNotExist(err) {
		// Only fall back if the central checkpoint itself is absent.
		if _, statErr := os.Lstat(s.recordPath(path)); os.IsNotExist(statErr) {
			return readRangeRecord(path)
		}
	}
	return record, err
}

func checkpointAccessError(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr) && !errors.Is(err, os.ErrNotExist)
}

func removeLegacyRangeFiles(path string, retry bool) {
	for _, suffix := range []string{".ranges.json", ".ranges"} {
		for _, name := range []string{path + suffix, filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+suffix)} {
			if info, err := os.Lstat(name); err == nil && info.Mode().IsRegular() {
				if retry {
					safeRemoveWithRetry(name)
				} else {
					_ = os.Remove(name)
				}
			}
		}
	}
}

// Publish the new checkpoint before removing the old metadata. A failed
// migration leaves the original media and sidecars available for retry.
func (s rangeStore) adoptLegacy(path string) error {
	if s == "" {
		return nil
	}
	_, recordErr := os.Lstat(rangeRecordPath(path))
	_, markerErr := os.Lstat(rangeMarkerPath(path))
	if os.IsNotExist(recordErr) && os.IsNotExist(markerErr) {
		return nil
	}
	checkpoint := s.recordPath(path)
	if _, err := os.Lstat(checkpoint); err == nil {
		if _, err := readRangeRecordAt(path, checkpoint); err == nil {
			removeLegacyRangeFiles(path, false)
		} else if checkpointAccessError(err) {
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	record, err := readRangeRecord(path)
	if err != nil {
		if checkpointAccessError(err) {
			return err
		}
		return nil // invalid checkpoints must be reset by the transfer
	}
	if err := s.write(path, record); err != nil {
		return err
	}
	removeLegacyRangeFiles(path, false)
	return nil
}

// This durable marker distinguishes a legacy contiguous prefix from a sparse
// file whose record was lost, including a crash during Windows replacement.
func ensureRangeMarker(path string) error {
	if _, err := os.Stat(rangeMarkerPath(path)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return utils.AtomicWriteFile(rangeMarkerPath(path), []byte("bbdown-ranges-v1\n"), 0600)
}

func (r rangeRecord) received() int64 {
	var n int64
	for _, b := range r.Ranges {
		n += b.Position - b.Start
	}
	return n
}

func readRangeRecord(path string) (rangeRecord, error) {
	return readRangeRecordAt(path, rangeRecordPath(path))
}

func readRangeRecordAt(path, checkpoint string) (rangeRecord, error) {
	var r rangeRecord
	info, err := os.Lstat(checkpoint)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return r, fmt.Errorf("下载进度记录无效或过大")
	}
	data, err := os.ReadFile(checkpoint)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Version != 1 || r.Total <= 0 || len(r.Ranges) == 0 || len(r.Ranges) > 4096 {
		return r, fmt.Errorf("下载进度记录无效")
	}
	var end int64
	for _, b := range r.Ranges {
		if b.Start != end || b.End <= b.Start || b.Position < b.Start || b.Position > b.End || b.End > r.Total {
			return r, fmt.Errorf("下载范围记录不连续或越界")
		}
		end = b.End
	}
	if end != r.Total {
		return r, fmt.Errorf("下载范围记录不完整")
	}
	info, err = os.Stat(path)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() {
		return r, fmt.Errorf("下载目标不是普通文件")
	}
	if info.Size() > r.Total {
		return r, fmt.Errorf("下载文件大于进度记录的总长度")
	}
	for _, b := range r.Ranges {
		if b.Position > b.Start && b.Position > info.Size() {
			return r, fmt.Errorf("下载文件短于已保存进度")
		}
	}
	return r, nil
}

func writeRangeRecord(path string, record rangeRecord) error {
	return rangeStore("").write(path, record)
}

func (s rangeStore) write(path string, record rangeRecord) error {
	if s == "" {
		if err := ensureRangeMarker(path); err != nil {
			return err
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(s.recordPath(path), data, 0600)
}

// Used before workers start and by progress reporting on resume. A damaged
// record must never make a preallocated file look complete.
func resumableStreamSize(path string) int64 {
	return rangeStore("").received(path)
}

func (s rangeStore) received(path string) int64 {
	if path == "" {
		return 0
	}
	if r, err := s.read(path); err == nil {
		return r.received()
	}
	if s != "" {
		return 0 // an untracked sparse file's length is never progress
	}
	if _, err := os.Stat(rangeRecordPath(path)); !os.IsNotExist(err) {
		return 0
	}
	if _, err := os.Stat(rangeMarkerPath(path)); !os.IsNotExist(err) {
		return 0
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}
