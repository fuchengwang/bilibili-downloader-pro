package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	Version      int         `json:"version"`
	Total        int64       `json:"total"`
	ETag         string      `json:"etag,omitempty"`
	LastModified string      `json:"lastModified,omitempty"`
	ValidatorKey string      `json:"validatorKey,omitempty"` // hash of origin/path, never a signed URL
	Ranges       []byteRange `json:"ranges"`
}

func rangeSidecarPath(path, suffix string) string {
	// Older records remain readable in place. New downloads keep these small
	// bookkeeping files hidden alongside the visible .downloading media.
	if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
		return path + suffix
	}
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+suffix)
}

func rangeRecordPath(path string) string { return rangeSidecarPath(path, ".ranges.json") }
func rangeMarkerPath(path string) string { return rangeSidecarPath(path, ".ranges") }

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
	var r rangeRecord
	data, err := os.ReadFile(rangeRecordPath(path))
	if err != nil {
		return r, err
	}
	if len(data) > 1024*1024 {
		return r, fmt.Errorf("下载进度记录过大")
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
	info, err := os.Stat(path)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() {
		return r, fmt.Errorf("下载目标不是普通文件")
	}
	for _, b := range r.Ranges {
		if b.Position > b.Start && b.Position > info.Size() {
			return r, fmt.Errorf("下载文件短于已保存进度")
		}
	}
	return r, nil
}

func writeRangeRecord(path string, record rangeRecord) error {
	if err := ensureRangeMarker(path); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(rangeRecordPath(path), data, 0600)
}

// Used before workers start and by progress reporting on resume. A damaged
// record must never make a preallocated file look complete.
func resumableStreamSize(path string) int64 {
	if path == "" {
		return 0
	}
	if r, err := readRangeRecord(path); err == nil {
		return r.received()
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
