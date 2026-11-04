package downloader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type collectionCleanupDir struct{ root, path string }

// No directory scans or recursive deletion: rmdir itself atomically refuses a
// nonempty directory, including hidden files and files added during cleanup.
// Only the one/two collection levels established at enqueue time are eligible.
func (m *DownloadManager) cleanupEmptyCollectionDirs(removed []*DownloadTask) {
	// Enqueue creates folders before publishing tasks. Exclude that window,
	// and protect remaining queued/paused tasks even if they have no files yet.
	m.addMu.Lock()
	defer m.addMu.Unlock()
	currentRoot := m.cfgMgr.Get().DownloadDir
	protected := make(map[string]bool)
	protect := func(path string) {
		if !filepath.IsAbs(path) {
			return
		}
		for path = filepath.Clean(path); ; path = filepath.Dir(path) {
			key := pathKey(path)
			if protected[key] {
				break
			}
			protected[key] = true
			if filepath.Dir(path) == path {
				break
			}
		}
	}
	protect(currentRoot)
	for _, task := range removed {
		if task != nil {
			protect(task.DownloadRoot)
		}
	}
	m.mu.RLock()
	for _, task := range m.tasks {
		if task != nil && task.OutputPath != "" {
			protect(filepath.Dir(task.OutputPath))
		}
	}
	m.mu.RUnlock()
	var registry map[string]string
	registryLoaded := false
	candidates := make(map[string]collectionCleanupDir)
	for _, task := range removed {
		if task == nil || !filepath.IsAbs(task.OutputPath) {
			continue
		}
		root, top := task.DownloadRoot, task.CollectionDir
		if root == "" && top == "" {
			// Legacy tasks have no recorded boundary. Adopt only a folder proven
			// by the existing stable collection index, never an arbitrary parent.
			if !filepath.IsAbs(currentRoot) || !pathWithin(currentRoot, task.OutputPath) || pathKey(filepath.Dir(task.OutputPath)) == pathKey(currentRoot) {
				continue
			}
			if !registryLoaded {
				registry = m.cleanupDirectoryRegistry()
				registryLoaded = true
			}
			identity := "video:" + task.BVID
			if task.CollectionID > 0 {
				identity = fmt.Sprintf("ugc:%d", task.CollectionID)
			}
			root = currentRoot
			top = registry[pathKey(root)+"|"+identity]
		}
		if !filepath.IsAbs(root) || !filepath.IsAbs(top) {
			continue
		}
		root, top = filepath.Clean(root), filepath.Clean(top)
		dir := filepath.Dir(filepath.Clean(task.OutputPath))
		if pathKey(filepath.Dir(top)) != pathKey(root) || pathKey(top) == pathKey(root) {
			continue
		}
		if pathKey(dir) != pathKey(top) && pathKey(filepath.Dir(dir)) != pathKey(top) {
			continue
		}
		for _, path := range []string{dir, top} {
			candidates[pathKey(path)] = collectionCleanupDir{root: root, path: path}
		}
	}
	dirs := make([]collectionCleanupDir, 0, len(candidates))
	for _, dir := range candidates {
		dirs = append(dirs, dir)
	}
	// Children must be removed before their owning collection, with each path
	// attempted once even when a batch contains thousands of episodes.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].path) > len(dirs[j].path) })
	currentInfo, _ := os.Stat(currentRoot)
	for _, dir := range dirs {
		if protected[pathKey(dir.path)] || !realCollectionPath(dir) {
			continue
		}
		info, err := os.Lstat(dir.path)
		if err != nil || (currentInfo != nil && os.SameFile(info, currentInfo)) {
			continue
		}
		// Unlike os.Remove, Rmdir can never unlink a regular file if another
		// process replaces this path between inspection and removal. On Windows
		// it calls RemoveDirectory; on macOS it calls rmdir. Failure is harmless.
		_ = syscall.Rmdir(dir.path)
	}
}

func realCollectionPath(dir collectionCleanupDir) bool {
	if info, err := os.Lstat(dir.root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	rel, err := filepath.Rel(dir.root, dir.path)
	if err != nil {
		return false
	}
	path := dir.root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return false
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func (m *DownloadManager) cleanupDirectoryRegistry() map[string]string {
	m.directoryMu.Lock()
	defer m.directoryMu.Unlock()
	if m.directoryRegistry != nil {
		registry := make(map[string]string, len(m.directoryRegistry))
		for key, value := range m.directoryRegistry {
			registry[key] = value
		}
		return registry
	}
	path := filepath.Join(filepath.Dir(m.cfgMgr.GetTasksPath()), "collection-directories.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var registry map[string]string
	if json.Unmarshal(data, &registry) != nil {
		return nil
	}
	return registry
}
