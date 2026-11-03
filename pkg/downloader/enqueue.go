package downloader

import (
	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/utils"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type downloadKey struct {
	cid             int64
	cheese, bangumi bool
	quality, codec  string
}

func taskDownloadKey(t *DownloadTask) downloadKey {
	return downloadKey{t.CID, t.IsCheese, t.IsBangumi, t.TargetQuality, t.TargetCodec}
}

func requestedDownloadKey(req *DownloadRequest, ep bilibili.EpisodeInfo) downloadKey {
	return downloadKey{ep.CID, req.IsCheese, req.IsBangumi, req.TargetQuality, req.TargetCodec}
}

func indexDownloadTasks(tasks []*DownloadTask, root string) map[downloadKey]*DownloadTask {
	index := make(map[downloadKey]*DownloadTask, len(tasks))
	for _, t := range tasks {
		if t == nil || (t.Status == StatusCompleted && !pathWithin(root, t.OutputPath)) {
			continue
		}
		key := taskDownloadKey(t)
		if index[key] == nil {
			index[key] = t
		}
	}
	return index
}

// Existing records are stored newest first for history. Assign old records in
// reverse list order once; subsequent scheduling uses this persisted sequence,
// including tasks created within the same second.
func assignMissingQueueOrders(tasks []*DownloadTask) uint64 {
	var next uint64
	for _, t := range tasks {
		if t != nil && t.QueueOrder > next {
			next = t.QueueOrder
		}
	}
	for i := len(tasks) - 1; i >= 0; i-- {
		if t := tasks[i]; t != nil && t.QueueOrder == 0 {
			next++
			t.QueueOrder = next
		}
	}
	return next
}

// A batch is prepared without holding the progress lock, then committed and
// saved once. The scheduler cannot see partially admitted or unsaved tasks.
// Returned task handles are internal; the app returns GetTasks snapshots to UI.
func (m *DownloadManager) AddDownloadTasks(req *DownloadRequest, episodes []bilibili.EpisodeInfo) ([]*DownloadTask, error) {
	if req == nil || len(episodes) == 0 {
		return nil, fmt.Errorf("未选择任何有效集数")
	}
	m.addMu.Lock()
	unlockAdd := sync.OnceFunc(m.addMu.Unlock)
	defer unlockAdd()
	cfg := m.cfgMgr.Get()
	if strings.TrimSpace(cfg.DownloadDir) == "" {
		return nil, fmt.Errorf("下载目录未设置")
	}
	if err := os.MkdirAll(cfg.DownloadDir, 0755); err != nil {
		return nil, fmt.Errorf("创建下载目录失败: %w", err)
	}
	known := indexDownloadTasks(m.GetTasks(), cfg.DownloadDir)
	prepared := make(map[downloadKey]*DownloadTask)
	for i := range episodes {
		key := requestedDownloadKey(req, episodes[i])
		if known[key] != nil || prepared[key] != nil {
			continue
		}
		task, err := m.prepareDownloadTask(req, &episodes[i], cfg)
		if err != nil {
			return nil, err
		}
		prepared[key] = task
	}
	added, modified, err := m.commitDownloadTasks(req, episodes, prepared, cfg.DownloadDir)
	if err != nil {
		return nil, err
	}
	unlockAdd()
	// Notify only after the complete transaction is durable. No task/status lock
	// is held during callbacks, so UI actions may safely reenter the manager.
	for _, task := range modified {
		m.notifyChange(task)
	}
	if len(modified) > 0 {
		m.triggerSchedule()
	}
	return added, nil
}

func (m *DownloadManager) commitDownloadTasks(req *DownloadRequest, episodes []bilibili.EpisodeInfo, prepared map[downloadKey]*DownloadTask, root string) (result, modified []*DownloadTask, resultErr error) {
	// Match SaveTasks' lock order; directory creation was prepared outside these locks.
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	before := append([]*DownloadTask(nil), m.tasks...)
	states := make(map[*DownloadTask]DownloadTask, len(before))
	changed := false
	for _, t := range before {
		if t != nil {
			states[t] = *t
			if t.QueueOrder == 0 {
				changed = true
			}
		}
	}
	defer func() {
		if resultErr != nil {
			m.tasks = before
			for task, state := range states {
				*task = state
			}
		}
	}()
	next := assignMissingQueueOrders(m.tasks)
	known := indexDownloadTasks(m.tasks, root)
	seen := make(map[downloadKey]bool)
	var newTasks []*DownloadTask
	for _, ep := range episodes {
		key := requestedDownloadKey(req, ep)
		if seen[key] {
			continue
		}
		seen[key] = true
		task := known[key]
		if task == nil {
			task = prepared[key]
			if task == nil {
				return nil, nil, fmt.Errorf("任务列表已变化，请重新添加这批任务")
			}
			next++
			task.QueueOrder = next
			newTasks = append(newTasks, task)
			modified = append(modified, task)
			known[key] = task
			changed = true
		} else if task.Status == StatusCompleted {
			info, err := os.Stat(task.OutputPath)
			if err != nil && !os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("检查已下载文件失败: %w", err)
			}
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				// A missing completed file is a new download request at the tail.
				next++
				task.QueueOrder = next
				task.Status, task.CompletedAt = StatusQueued, 0
				task.Progress, task.DownloadedBytes, task.Speed = 0, 0, 0
				task.SpeedStr, task.SizeStr, task.ErrorMsg = "0 KB/s", "准备中...", ""
				task.ResumeOnStartup = false
				modified = append(modified, task)
				changed = true
			}
		} else if task.Status == StatusPaused || task.Status == StatusError || task.Status == StatusCancelled {
			// Resume/retry keeps its established place and its resumable files.
			task.Status, task.ErrorMsg, task.ResumeOnStartup = StatusQueued, "", false
			modified = append(modified, task)
			changed = true
		}
		result = append(result, task)
	}
	if len(newTasks) > 0 {
		// Preserve newest-first history without repeatedly copying the whole list.
		combined := make([]*DownloadTask, len(newTasks)+len(m.tasks))
		for i, task := range newTasks {
			combined[len(newTasks)-1-i] = task
		}
		copy(combined[len(newTasks):], m.tasks)
		m.tasks = combined
	}
	if changed {
		if err := m.writeTaskSnapshot(m.tasks); err != nil {
			return nil, nil, fmt.Errorf("保存下载任务失败: %w", err)
		}
	}
	return result, modified, nil
}

func (m *DownloadManager) resumeTasks(id string, all bool) error {
	modified, err := func() ([]*DownloadTask, error) {
		m.saveMu.Lock()
		defer m.saveMu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		var modified []*DownloadTask
		before := make(map[*DownloadTask]DownloadTask)
		for _, task := range m.tasks {
			if task == nil || (!all && task.ID != id) {
				continue
			}
			if task.Status == StatusPaused || task.Status == StatusError || (!all && task.Status == StatusCancelled) {
				before[task] = *task
				task.Status, task.ErrorMsg, task.ResumeOnStartup = StatusQueued, "", false
				modified = append(modified, task)
			}
		}
		if len(modified) > 0 {
			if err := m.writeTaskSnapshot(m.tasks); err != nil {
				for task, state := range before {
					*task = state
				}
				return nil, err
			}
		}
		return modified, nil
	}()
	if err != nil {
		return err
	}
	for _, task := range modified {
		m.notifyChange(task)
	}
	if len(modified) > 0 {
		m.triggerSchedule()
	}
	return nil
}

func (m *DownloadManager) prepareDownloadTask(req *DownloadRequest, ep *bilibili.EpisodeInfo, cfg config.Settings) (*DownloadTask, error) {
	// 生成规范的输出路径与文件名（支持极端特殊字符与超长标题安全降级）
	sanitizedTitle := utils.SanitizeFilename(req.Title, "bilibili_video")
	var outDir string
	var baseFileName string

	// 判断是否为多集/合集/多P视频
	isMulti := req.IsCollection || req.CollectionID > 0 || len(req.Episodes) > 1 || req.IsBangumi || req.IsCheese || strings.Contains(ep.Badge, "合集") || ep.Index > 1 || (ep.Title != "" && ep.Title != req.Title)

	if isMulti {
		// 合集和多P按稳定身份复用目录；同名但不同来源的内容保持隔离。
		collFolder := sanitizedTitle
		if req.CollectionID > 0 {
			var err error
			outDir, err = m.collectionDir(cfg.DownloadDir, fmt.Sprintf("ugc:%d", req.CollectionID), sanitizedTitle, 2, func() []string { return m.legacyCollectionDirs(cfg.DownloadDir, req, ep, false) })
			if err == nil && ep.PartCount > 1 {
				outDir, err = m.collectionDir(outDir, "video:"+ep.BVID, collectionVideoName(ep.Index, ep.VideoTitle), 1, func() []string { return m.legacyCollectionDirs(outDir, req, ep, true) })
			}
			if err != nil {
				return nil, fmt.Errorf("创建合集下载目录失败: %w", err)
			}
		} else if req.IsCheese && req.SSID > 0 {
			collFolder = fmt.Sprintf("%s [ss%d]", sanitizedTitle, req.SSID)
		} else if !req.IsBangumi && ep.BVID != "" {
			var err error
			outDir, err = m.collectionDir(cfg.DownloadDir, "video:"+ep.BVID, sanitizedTitle, 1, func() []string { return m.legacyCollectionDirs(cfg.DownloadDir, req, ep, false) })
			if err != nil {
				return nil, fmt.Errorf("创建视频下载目录失败: %w", err)
			}
		}
		if outDir == "" {
			outDir = filepath.Join(cfg.DownloadDir, collFolder)
		}
		fileIndex := ep.Index
		if req.CollectionID > 0 && ep.PartCount > 1 {
			fileIndex = ep.Page
		}
		partName := formatFileNameByTemplate(cfg.FileNameTemplate, req.Title, ep.Title, ep.BVID, fileIndex)
		if req.CollectionID > 0 && ep.VideoTitle != "" && cfg.FileNameTemplate == "{title} - {part}" {
			if ep.PartCount > 1 {
				partName = fmt.Sprintf("P%02d. %s", ep.Page, ep.Title)
			} else {
				partName = collectionVideoName(ep.Index, ep.VideoTitle)
			}
		}
		fallbackPart := fmt.Sprintf("P%02d", ep.Index)
		if ep.Index <= 0 {
			fallbackPart = "video"
		}
		baseFileName = utils.SanitizeFilename(partName, fallbackPart)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return nil, fmt.Errorf("创建合集下载目录失败: %w", err)
		}
	} else {
		// 单视频：直接保存在下载主目录
		outDir = cfg.DownloadDir
		partName := formatFileNameByTemplate(cfg.FileNameTemplate, req.Title, ep.Title, ep.BVID, ep.Index)
		baseFileName = utils.SanitizeFilename(partName, sanitizedTitle)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return nil, fmt.Errorf("创建下载目录失败: %w", err)
		}
	}

	// 确保在 Windows 260 字符限制下，完整输出路径保持在安全阈值内 (<= 240 字符)
	outPath := utils.EnsureSafePathLength(outDir, baseFileName, ".mp4")

	taskID := newUUID()
	// The worker reserves the final quality/collision suffix before writing.
	vTmp, aTmp := downloadPaths(outPath)

	task := &DownloadTask{
		ID:            taskID,
		BVID:          ep.BVID,
		AID:           ep.AID,
		CID:           ep.CID,
		EPID:          ep.EPID,
		IsBangumi:     req.IsBangumi,
		IsCheese:      req.IsCheese,
		Title:         req.Title,
		PartTitle:     ep.Title,
		CollectionID:  req.CollectionID,
		VideoTitle:    ep.VideoTitle,
		PartPage:      ep.Page,
		PartCount:     ep.PartCount,
		Cover:         ep.Cover,
		SourceURL:     bilibili.EpisodeURL(ep, req.IsBangumi, req.IsCheese),
		TargetQuality: req.TargetQuality,
		TargetCodec:   req.TargetCodec,
		Status:        StatusQueued,
		Progress:      0,
		SpeedStr:      "0 KB/s",
		SizeStr:       "准备中...",
		Duration:      ep.Duration,
		CreatedAt:     time.Now().Unix(),
		OutputPath:    outPath,
		VideoTmpPath:  vTmp,
		AudioTmpPath:  aTmp,
	}
	if req.CollectionID > 0 {
		task.CollectionTitle = req.Title
		if ep.PartCount == 1 {
			task.PartTitle = ep.VideoTitle
		}
	}

	return task, nil
}
