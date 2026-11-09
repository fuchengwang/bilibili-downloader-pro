package downloader

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/utils"
)

// Directory I/O has its own lock: a slow destination must not hold the task
// status/progress lock. Read the registry once, write only new associations.
func (m *DownloadManager) collectionDir(parent, identity, name string, levels int, legacy func() []string) (string, error) {
	m.directoryMu.Lock()
	defer m.directoryMu.Unlock()
	registryPath := filepath.Join(filepath.Dir(m.cfgMgr.GetTasksPath()), "collection-directories.json")
	if m.directoryRegistry == nil {
		m.directoryRegistry = map[string]string{}
		m.directoryWritable = true
		data, err := os.ReadFile(registryPath)
		if err == nil {
			var loaded map[string]string
			if err := json.Unmarshal(data, &loaded); err != nil || loaded == nil {
				// Preserve malformed content before rebuilding. If even the
				// backup cannot be written, use a session-only registry.
				backup := registryPath + ".invalid-" + newUUID() + ".json"
				if err := os.WriteFile(backup, data, 0600); err != nil {
					m.directoryWritable = false
				}
				log.Printf("合集目录索引异常，使用历史记录恢复关联")
			} else {
				m.directoryRegistry = loaded
			}
		} else if !os.IsNotExist(err) {
			m.directoryWritable = false
			log.Printf("合集目录索引暂不可读，使用会话内关联: %v", err)
		}
	}
	registry := m.directoryRegistry
	key := pathKey(parent) + "|" + identity
	claimed := make(map[string]bool)
	for other, path := range registry {
		if other != key {
			claimed[storagePathKey(path)] = true
		}
	}
	if path, ok := registry[key]; ok {
		if filepath.Dir(pathKey(path)) == pathKey(parent) && !claimed[storagePathKey(path)] {
			if err := os.MkdirAll(path, 0755); err != nil {
				return "", err
			}
			return path, nil
		}
		// An invalid individual entry must not poison the entire registry.
		delete(registry, key)
	}
	remember := func(path string) (string, error) {
		registry[key] = path
		if m.directoryWritable {
			data, err := json.MarshalIndent(registry, "", "  ")
			if err == nil {
				err = utils.AtomicWriteFile(registryPath, data, 0644)
			}
			if err != nil {
				// Task history also retains the output directory; failure to
				// write this auxiliary index must not reject the download.
				log.Printf("合集目录关联暂未保存，保留会话内关联: %v", err)
			}
		}
		return path, nil
	}
	candidates := map[string]string{}
	var legacyPaths []string
	if legacy != nil {
		legacyPaths = legacy()
	}
	for _, path := range legacyPaths {
		if filepath.Dir(pathKey(path)) != pathKey(parent) {
			continue
		}
		if claimed[storagePathKey(path)] {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			candidates[pathKey(path)] = path
		}
	}
	// Ambiguous histories must never arbitrarily pick one of several folders.
	if len(candidates) == 1 {
		for _, path := range candidates {
			return remember(path)
		}
	}
	name = utils.SanitizeFilename(name, "bilibili_collection")
	// Budget every remaining layer, including separators and collision suffixes.
	// The parent collection always reserves a child, even if its first task
	// happens to be a video with only one part.
	maxName := (175-utils.WindowsPathUnits(parent))/max(1, levels) - 7
	if maxName > 55 {
		maxName = 55
	}
	if maxName < 8 {
		return "", fmt.Errorf("下载目录路径过长，请选择较短的下载目录")
	}
	name = utils.SanitizeFilename(utils.TruncatePathUnits(name, maxName), "collection")
	for i := 0; i < 10000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)", name, i+1)
		}
		path := filepath.Join(parent, candidate)
		// Empty folders may have been removed without releasing their identity.
		if claimed[storagePathKey(path)] {
			continue
		}
		if err := os.Mkdir(path, 0755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}
		return remember(path)
	}
	return "", fmt.Errorf("同名合集目录过多")
}

// Recover only directories whose media identity is established by saved tasks.
// No files are moved, and an unrelated same-name directory is never adopted.
func (m *DownloadManager) legacyCollectionDirs(parent string, req *DownloadRequest, ep *bilibili.EpisodeInfo, child bool) []string {
	var paths []string
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, task := range m.tasks {
		if task == nil || task.OutputPath == "" || task.IsBangumi || task.IsCheese {
			continue
		}
		dir := filepath.Dir(task.OutputPath)
		if req.CollectionID > 0 {
			if task.CollectionID == req.CollectionID {
				if child {
					if task.BVID != ep.BVID || task.PartCount <= 1 {
						continue
					}
				} else if task.PartCount > 1 {
					dir = filepath.Dir(dir)
				}
			} else if !child && task.CollectionID == 0 && task.BVID == ep.BVID && task.Title == req.Title {
				// Old UGC records had no season ID. A verified member BV plus
				// the same collection title identifies this legacy directory.
			} else {
				continue
			}
		} else if task.CollectionID != 0 || task.BVID != ep.BVID {
			continue
		}
		if filepath.Dir(pathKey(dir)) == pathKey(parent) {
			paths = append(paths, dir)
		}
	}
	return paths
}

func collectionVideoName(index int, title string) string {
	if index > 0 {
		return fmt.Sprintf("%02d. %s", index, strings.TrimSpace(title))
	}
	return title
}
