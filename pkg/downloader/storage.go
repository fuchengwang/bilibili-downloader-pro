package downloader

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"bilibili_downloader/pkg/utils"
)

func pathKey(path string) string {
	key := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

func pathWithin(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(pathKey(root), pathKey(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

var taskIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (m *DownloadManager) cachePaths(id string) (string, string) {
	// IDs from persisted data must never escape the cache root.
	if !taskIDPattern.MatchString(id) {
		id = fmt.Sprintf("legacy-%x", []byte(id))
	}
	dir := filepath.Join(m.cfgMgr.GetDownloadCacheDir(), id)
	return filepath.Join(dir, "video.downloading"), filepath.Join(dir, "audio.downloading")
}

func cleanupStreamArtifacts(path string) {
	if path == "" {
		return
	}
	for i := 0; i < 8; i++ {
		safeRemoveWithRetry(streamSegmentPath(path, i))
	}
	safeRemoveWithRetry(path)
	// Remove only an empty task directory, never recursively delete user files.
	if filepath.Base(path) == "video.downloading" || filepath.Base(path) == "audio.downloading" {
		_ = os.Remove(filepath.Dir(path))
	}
}

// availableOutputPathLocked reserves against both other tasks and existing files.
// It runs after stream selection, when the final quality suffix is known.
func (m *DownloadManager) availableOutputPathLocked(path, taskID string) (string, error) {
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), ".mp4")
	for i := 0; i < 10000; i++ {
		candidate := path
		if i > 0 {
			candidate = utils.EnsureSafePathLength(dir, base, fmt.Sprintf(" (%d).mp4", i))
		}
		occupied := false
		for _, t := range m.tasks {
			if t != nil && t.ID != taskID && pathKey(t.OutputPath) == pathKey(candidate) {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("检查输出文件失败: %w", err)
		}
	}
	return "", fmt.Errorf("同名文件过多，无法分配输出文件名")
}

type migratedStream struct{ old, next string }

func migrateStream(old, next string) error {
	if old == "" || old == next {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(next), 0755); err != nil {
		return err
	}
	paths := []migratedStream{{old, next}}
	for i := 0; i < 8; i++ {
		paths = append(paths, migratedStream{streamSegmentPath(old, i), streamSegmentPath(next, i)})
	}
	for _, pair := range paths {
		if _, err := os.Stat(pair.old); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		// Preserve the source until the new task paths have been persisted. This
		// also handles download directories on a different volume from the cache.
		if err := copyPreservingSourceContext(context.Background(), pair.old, pair.next); err != nil {
			return err
		}
	}
	return nil
}

// Called before the scheduler starts: repair old histories and migrate resumable
// files without touching files which still belong to a pending task.
func (m *DownloadManager) reconcileTaskStorage() {
	original := m.tasks
	if len(original) == 0 {
		m.cleanupLegacyParts(nil)
		m.cleanupCacheOrphans()
		return
	}
	var normalized []*DownloadTask
	completed := make(map[string]*DownloadTask)
	var migrated []migratedStream
	for _, task := range original {
		t := *task
		if t.Status == StatusCompleted && t.OutputPath != "" {
			key := pathKey(t.OutputPath)
			if kept := completed[key]; kept != nil {
				if t.CompletedAt > kept.CompletedAt {
					*kept = t
				}
				continue
			}
			completed[key] = &t
		}
		if t.Status != StatusCompleted {
			video, audio := m.cachePaths(t.ID)
			if t.VideoTmpPath != video || t.AudioTmpPath != audio {
				if err := migrateStream(t.VideoTmpPath, video); err != nil {
					log.Printf("迁移视频临时文件失败，保留原路径: %v", err)
				} else if err := migrateStream(t.AudioTmpPath, audio); err != nil {
					log.Printf("迁移音频临时文件失败，保留原路径: %v", err)
				} else {
					migrated = append(migrated, migratedStream{t.VideoTmpPath, video}, migratedStream{t.AudioTmpPath, audio})
					t.VideoTmpPath, t.AudioTmpPath = video, audio
				}
			}
		}
		normalized = append(normalized, &t)
	}
	m.tasks = normalized
	if err := m.SaveTasks(); err != nil {
		m.tasks = original
		log.Printf("保存任务整理结果失败，保留原文件: %v", err)
		return
	}
	for _, pair := range migrated {
		if pair.old != pair.next {
			cleanupStreamArtifacts(pair.old)
		}
	}
	m.cleanupLegacyParts(original)
	m.cleanupCacheOrphans()
}

func (m *DownloadManager) cleanupCacheOrphans() {
	root := m.cfgMgr.GetDownloadCacheDir()
	protected := make(map[string]bool)
	for _, task := range m.tasks {
		protected[pathKey(filepath.Dir(task.VideoTmpPath))] = true
		protected[pathKey(filepath.Dir(task.AudioTmpPath))] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !taskIDPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if protected[pathKey(dir)] {
			continue
		}
		cleanupStreamArtifacts(filepath.Join(dir, "video.downloading"))
		cleanupStreamArtifacts(filepath.Join(dir, "audio.downloading"))
	}
}

var legacyPartName = regexp.MustCompile(`\.[0-9a-f]{8}\.(video|audio)\.downloading\.part-0[0-7]$`)

func (m *DownloadManager) cleanupLegacyParts(original []*DownloadTask) {
	protected := make(map[string]bool)
	dirs := map[string]bool{m.cfgMgr.Get().DownloadDir: true}
	for _, t := range m.tasks {
		if t.Status != StatusCompleted {
			protected[pathKey(t.VideoTmpPath)] = true
			protected[pathKey(t.AudioTmpPath)] = true
		}
	}
	for _, t := range original {
		for _, path := range []string{t.VideoTmpPath, t.AudioTmpPath} {
			if path == "" {
				continue
			}
			dirs[filepath.Dir(path)] = true
			if !protected[pathKey(path)] && t.Status == StatusCompleted {
				if m.cfgMgr.Get().DeleteTempFiles {
					cleanupStreamArtifacts(path)
				} else {
					for i := 0; i < 8; i++ {
						safeRemoveWithRetry(streamSegmentPath(path, i))
					}
				}
			}
		}
	}
	for dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !legacyPartName.MatchString(entry.Name()) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			base := path[:len(path)-len(".part-00")]
			if !protected[pathKey(base)] {
				safeRemoveWithRetry(path)
			}
		}
	}
}
