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

// Both tracks live next to the final MP4. Their count is independent of the
// number of network workers, and neither can be mistaken for a finished MP4.
func downloadPaths(output string) (string, string) {
	return output + ".downloading", strings.TrimSuffix(output, ".mp4") + ".audio.downloading"
}

// Also supplies private paths for queued tasks before the output name is reserved.
func (m *DownloadManager) cachePaths(id string) (string, string) {
	// IDs from persisted data must never escape the cache root.
	if !taskIDPattern.MatchString(id) {
		id = fmt.Sprintf("legacy-%x", []byte(id))
	}
	dir := filepath.Join(m.cfgMgr.GetDownloadCacheDir(), id)
	return filepath.Join(dir, "video.downloading"), filepath.Join(dir, "audio.downloading")
}

func storagePathKey(path string) string {
	// Default macOS volumes are case insensitive. Conservatively reserve case
	// variants even on case-sensitive volumes.
	if runtime.GOOS == "darwin" {
		return strings.ToLower(pathKey(path))
	}
	return pathKey(path)
}

func (m *DownloadManager) holdStoragePathsLocked(paths ...string) {
	if m.retiringPaths == nil {
		m.retiringPaths = make(map[string]int)
	}
	for _, path := range paths {
		if path != "" {
			m.retiringPaths[storagePathKey(path)]++
		}
	}
}

func (m *DownloadManager) releaseStoragePaths(paths ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, path := range paths {
		if path == "" {
			continue
		}
		key := storagePathKey(path)
		if m.retiringPaths[key] > 1 {
			m.retiringPaths[key]--
		} else {
			delete(m.retiringPaths, key)
		}
	}
}

// Reserve the paths we are going to remove before publishing the removal of
// the task. They stay unavailable until its worker and filesystem cleanup end.
type taskStorageCleanup struct {
	DownloadTask
	deleteOutput bool
	workerDone   <-chan struct{}
}

func (m *DownloadManager) removeTasksForCleanup(match func(*DownloadTask) bool, deleteFile bool) ([]taskStorageCleanup, error) {
	// Publish removal and reserve its paths in the same transaction. A failed
	// save must leave the tasks and their active workers in place.
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	remaining := make([]*DownloadTask, 0, len(m.tasks))
	var removed []*DownloadTask
	for _, task := range m.tasks {
		if task == nil {
			continue
		}
		if match(task) {
			removed = append(removed, task)
		} else {
			remaining = append(remaining, task)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := m.writeTaskSnapshot(remaining); err != nil {
		return nil, err
	}
	m.tasks = remaining
	occupied := make(map[string]bool)
	for _, task := range remaining {
		for _, path := range []string{task.VideoTmpPath, task.AudioTmpPath} {
			if path != "" {
				occupied[storagePathKey(path)] = true
			}
		}
		// A proposed filename on a queued task is not ownership of a
		// completed file left by another task.
		if task.OutputPath != "" && (task.Status == StatusCompleted || task.Status == StatusMerging) {
			occupied[storagePathKey(task.OutputPath)] = true
		}
	}
	cleanup := make([]taskStorageCleanup, 0, len(removed))
	for _, task := range removed {
		plan := m.reserveTaskCleanupLocked(task, deleteFile, occupied)
		if worker, ok := m.workers[task.ID]; ok {
			plan.workerDone = worker.done
			if worker.cancel != nil {
				worker.cancel()
			}
			worker.invalidated = true
			m.workers[task.ID] = worker
		}
		cleanup = append(cleanup, plan)
	}
	return cleanup, nil
}

func (m *DownloadManager) reserveTaskCleanupLocked(task *DownloadTask, deleteFile bool, occupied map[string]bool) taskStorageCleanup {
	exclusive := func(path string) bool {
		key := storagePathKey(path)
		return path != "" && m.retiringPaths[key] == 0 && !occupied[key]
	}
	cleanup := taskStorageCleanup{
		DownloadTask: *task,
		deleteOutput: deleteFile && task.Status == StatusCompleted && task.OutputPath != "" && !occupied[storagePathKey(task.OutputPath)],
	}
	if !exclusive(task.VideoTmpPath) {
		cleanup.VideoTmpPath = ""
	}
	if !exclusive(task.AudioTmpPath) {
		cleanup.AudioTmpPath = ""
	}
	// Also reserve the output of a removed worker which may still be rolling
	// back a migration into the output directory.
	m.holdStoragePathsLocked(cleanup.VideoTmpPath, cleanup.AudioTmpPath, cleanup.OutputPath)
	return cleanup
}

func (m *DownloadManager) cleanReservedTaskFiles(task *taskStorageCleanup) {
	defer m.releaseStoragePaths(task.VideoTmpPath, task.AudioTmpPath, task.OutputPath)
	if task.workerDone != nil {
		<-task.workerDone
	}
	cleanupStreamArtifacts(task.VideoTmpPath)
	cleanupStreamArtifacts(task.AudioTmpPath)
	if task.deleteOutput {
		safeRemoveWithRetry(task.OutputPath)
	}
}

func cleanupStreamArtifacts(path string) {
	if path == "" {
		return
	}
	for i := 0; i < 8; i++ {
		safeRemoveWithRetry(streamSegmentPath(path, i))
	}
	safeRemoveWithRetry(path)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		safeRemoveWithRetry(rangeRecordPath(path))
		safeRemoveWithRetry(rangeMarkerPath(path))
	}
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
	occupiedPaths := make(map[string]bool)
	ownedStreams := make(map[string]bool, 2)
	for key := range m.retiringPaths {
		occupiedPaths[key] = true
	}
	for _, task := range m.tasks {
		if task == nil {
			continue
		}
		if task.ID == taskID {
			ownedStreams[storagePathKey(task.VideoTmpPath)] = task.VideoTmpPath != ""
			ownedStreams[storagePathKey(task.AudioTmpPath)] = task.AudioTmpPath != ""
			continue
		}
		for _, path := range []string{task.OutputPath, task.VideoTmpPath, task.AudioTmpPath} {
			if path != "" {
				occupiedPaths[storagePathKey(path)] = true
			}
		}
	}
	for i := 0; i < 10000; i++ {
		candidate := path
		if i > 0 {
			candidate = utils.EnsureSafePathLength(dir, base, fmt.Sprintf(" (%d).mp4", i))
		}
		video, audio := downloadPaths(candidate)
		if occupiedPaths[storagePathKey(candidate)] || occupiedPaths[storagePathKey(video)] || occupiedPaths[storagePathKey(audio)] {
			continue
		}
		occupied := false
		for _, staged := range []string{video, audio} {
			if ownedStreams[storagePathKey(staged)] {
				continue
			}
			artifacts := []string{staged, rangeRecordPath(staged), rangeMarkerPath(staged)}
			for i := 0; i < 8; i++ {
				artifacts = append(artifacts, streamSegmentPath(staged, i))
			}
			for _, artifact := range artifacts {
				if _, err := os.Lstat(artifact); err == nil {
					occupied = true
					break
				} else if !os.IsNotExist(err) {
					return "", fmt.Errorf("检查下载中文件失败: %w", err)
				}
			}
			if occupied {
				break
			}
		}
		if occupied {
			continue
		}
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("检查输出文件失败: %w", err)
		}
	}
	return "", fmt.Errorf("同名文件过多，无法分配输出文件名")
}

type migratedStream struct{ old, next string }

func migrateStream(old, next string) error {
	return migrateStreamContext(context.Background(), old, next)
}

func migrateStreamContext(ctx context.Context, old, next string) error {
	_, err := migrateStreamArtifacts(ctx, old, next)
	return err
}

func migrateStreamArtifacts(ctx context.Context, old, next string) ([]string, error) {
	if old == "" || old == next {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(next), 0755); err != nil {
		return nil, err
	}
	paths := []migratedStream{{old, next}}
	paths = append(paths, migratedStream{rangeRecordPath(old), rangeRecordPath(next)})
	paths = append(paths, migratedStream{rangeMarkerPath(old), rangeMarkerPath(next)})
	for i := 0; i < 8; i++ {
		paths = append(paths, migratedStream{streamSegmentPath(old, i), streamSegmentPath(next, i)})
	}
	var created []string
	for _, pair := range paths {
		if _, err := os.Stat(pair.old); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return created, err
		}
		if _, err := os.Stat(pair.next); err == nil {
			return created, fmt.Errorf("目标下载中文件已存在，保留原缓存: %s", pair.next)
		} else if !os.IsNotExist(err) {
			return created, err
		}
		// Preserve the source until the new task paths have been persisted. This
		// also handles download directories on a different volume from the cache.
		if err := copyStreamArtifact(ctx, pair.old, pair.next); err != nil {
			return created, err
		}
		created = append(created, pair.next)
	}
	return created, nil
}

// Do not apply completed-media normalization to a partially downloaded file:
// range positions and the raw bytes must survive migration exactly.
func copyStreamArtifact(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("下载缓存不是普通文件: %s", src)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".bbdown-migrate-*")
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := copyWithContext(ctx, tmp, f, make([]byte, 1024*1024)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Called by the owning worker after the final name has been reserved. Copy
// first, persist the new paths, then remove old caches; a failed save or a
// cancellation must leave the old resumable state intact.
func (m *DownloadManager) relocateTaskStreams(ctx context.Context, task *DownloadTask) error {
	m.mu.RLock()
	video, audio := downloadPaths(task.OutputPath)
	pairs := []migratedStream{{task.VideoTmpPath, video}, {task.AudioTmpPath, audio}}
	m.mu.RUnlock()
	changed := false
	var created []string
	rollback := func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
	}
	for _, pair := range pairs {
		if pair.old == pair.next {
			continue
		}
		changed = true
		copied, err := migrateStreamArtifacts(ctx, pair.old, pair.next)
		created = append(created, copied...)
		if err != nil {
			rollback()
			return err
		}
	}
	if !changed {
		return m.SaveTasks()
	}
	m.saveMu.Lock()
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		m.saveMu.Unlock()
		rollback()
		return err
	}
	task.VideoTmpPath, task.AudioTmpPath = video, audio
	// The write is short and serialized with all other task snapshots. Do not
	// publish new paths to another saver until they are durably committed.
	err := m.writeTaskSnapshot(m.tasks)
	if err != nil {
		task.VideoTmpPath, task.AudioTmpPath = pairs[0].old, pairs[1].old
	} else {
		for _, pair := range pairs {
			if pair.old != pair.next {
				m.holdStoragePathsLocked(pair.old)
			}
		}
	}
	m.mu.Unlock()
	m.saveMu.Unlock()
	if err != nil {
		rollback()
		return err
	}
	for _, pair := range pairs {
		if pair.old != pair.next {
			cleanupStreamArtifacts(pair.old)
			m.releaseStoragePaths(pair.old)
		}
	}
	return nil
}

// Called before the scheduler starts: repair old histories and preserve pending
// files in place. The owning worker migrates them when the task resumes.
func (m *DownloadManager) reconcileTaskStorage() {
	original := m.tasks
	if len(original) == 0 {
		m.cleanupLegacyParts(nil, nil)
		m.cleanupCacheOrphans(nil)
		return
	}
	var normalized []*DownloadTask
	completed := make(map[string]*DownloadTask)
	streamRefs := make(map[string]int)
	for _, task := range original {
		for _, path := range []string{task.VideoTmpPath, task.AudioTmpPath} {
			if path != "" {
				streamRefs[storagePathKey(path)]++
			}
		}
	}
	detachSharedStreams := func(task *DownloadTask) {
		for _, path := range []*string{&task.VideoTmpPath, &task.AudioTmpPath} {
			if streamRefs[storagePathKey(*path)] > 1 {
				*path = ""
			}
		}
	}
	for _, task := range original {
		t := *task
		// Old queued tasks could inherit another task's filename before it
		// reserved the path. Neither record proves which bytes are its own.
		detachSharedStreams(&t)
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
		// Keep pending caches where they are until the worker resumes. Large
		// copies or an unavailable destination must never block app startup.
		normalized = append(normalized, &t)
	}
	m.tasks = normalized
	if err := m.SaveTasks(); err != nil {
		m.tasks = original
		for _, task := range original {
			detachSharedStreams(task)
		}
		log.Printf("保存任务整理结果失败，保留原文件: %v", err)
		return
	}
	m.cleanupLegacyParts(original, streamRefs)
	m.cleanupCacheOrphans(streamRefs)
}

func (m *DownloadManager) cleanupCacheOrphans(streamRefs map[string]int) {
	root := m.cfgMgr.GetDownloadCacheDir()
	protected := make(map[string]bool)
	for _, task := range m.tasks {
		protected[storagePathKey(filepath.Dir(task.VideoTmpPath))] = true
		protected[storagePathKey(filepath.Dir(task.AudioTmpPath))] = true
	}
	for path, count := range streamRefs {
		if count > 1 {
			protected[filepath.Dir(path)] = true
		}
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
		if protected[storagePathKey(dir)] {
			continue
		}
		cleanupStreamArtifacts(filepath.Join(dir, "video.downloading"))
		cleanupStreamArtifacts(filepath.Join(dir, "audio.downloading"))
	}
}

var legacyPartName = regexp.MustCompile(`\.[0-9a-f]{8}\.(video|audio)\.downloading\.part-0[0-7]$`)

func (m *DownloadManager) cleanupLegacyParts(original []*DownloadTask, streamRefs map[string]int) {
	protected := make(map[string]bool)
	// Only inspect directories referenced by legacy task caches. Opening the
	// user's download destination unconditionally can block startup on macOS
	// protected folders or disconnected volumes, even with an empty queue.
	dirs := make(map[string]bool)
	for _, t := range m.tasks {
		if t.Status != StatusCompleted {
			protected[storagePathKey(t.VideoTmpPath)] = true
			protected[storagePathKey(t.AudioTmpPath)] = true
		}
	}
	for _, t := range original {
		for _, path := range []string{t.VideoTmpPath, t.AudioTmpPath} {
			if path == "" || streamRefs[storagePathKey(path)] > 1 {
				continue
			}
			if legacyPartName.MatchString(filepath.Base(path) + ".part-00") {
				dirs[filepath.Dir(path)] = true
			}
			if !protected[storagePathKey(path)] && t.Status == StatusCompleted {
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
			if !protected[storagePathKey(base)] && streamRefs[storagePathKey(base)] <= 1 {
				safeRemoveWithRetry(path)
			}
		}
	}
}
