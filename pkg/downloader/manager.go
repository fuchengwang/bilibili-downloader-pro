package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/utils"
)

// ProgressCallback 进度通知函数
type ProgressCallback func(t *DownloadTask)

type workerHandle struct {
	token  string
	cancel context.CancelFunc
}

// DownloadManager 负责整个下载队列、并发调度与状态管理
type DownloadManager struct {
	mu           sync.RWMutex
	tasks        []*DownloadTask
	workers      map[string]workerHandle
	callback     ProgressCallback
	biliClient   *bilibili.Client
	cfgMgr       *config.ConfigManager
	workerNotify chan struct{}
}

var (
	managerInstance *DownloadManager
	managerOnce     sync.Once
)

// NewDownloadManager 创建一个独立的下载管理器实例（用于测试与沙箱隔离）
func NewDownloadManager(cfgMgr *config.ConfigManager) *DownloadManager {
	m := &DownloadManager{
		tasks:        make([]*DownloadTask, 0),
		workers:      make(map[string]workerHandle),
		biliClient:   bilibili.GetDefaultClient(),
		cfgMgr:       cfgMgr,
		workerNotify: make(chan struct{}, 50),
	}
	m.loadTasks()
	return m
}

// GetManager 获取全局下载管理器单例
func GetManager() *DownloadManager {
	managerOnce.Do(func() {
		m := NewDownloadManager(config.GetManager())
		go m.schedulerLoop()
		managerInstance = m
	})
	return managerInstance
}

// SetCallback 设置任务状态变化时的全局回调
func (m *DownloadManager) SetCallback(cb ProgressCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callback = cb
}

func (m *DownloadManager) notifyChange(t *DownloadTask) {
	if t == nil {
		return
	}
	m.mu.RLock()
	cb := m.callback
	tCopy := *t // 在锁保护内安全深/浅拷贝，彻底消除数据竞争
	m.mu.RUnlock()
	if cb != nil {
		cb(&tCopy)
	}
}

func (m *DownloadManager) loadTasks() {
	tasksPath := m.cfgMgr.GetTasksPath()
	data, err := os.ReadFile(tasksPath)
	if err != nil {
		return
	}
	var loaded []*DownloadTask
	if err := json.Unmarshal(data, &loaded); err == nil {
		for _, t := range loaded {
			// 将未完成的任务重置为暂停或队列状态
			if t.Status == StatusDownloading || t.Status == StatusMerging {
				t.Status = StatusPaused
				t.Speed = 0
				t.SpeedStr = "0 KB/s"
			}
		}
		m.tasks = loaded
	}
}

// SaveTasks 持久化任务列表 (原子写入避免崩溃产生损坏文件，杜绝 Windows 权限冲突)
func (m *DownloadManager) SaveTasks() {
	m.mu.RLock()
	tasksCopy := make([]*DownloadTask, len(m.tasks))
	for i, t := range m.tasks {
		tCopy := *t
		tasksCopy[i] = &tCopy
	}
	m.mu.RUnlock()

	tasksPath := m.cfgMgr.GetTasksPath()
	data, err := json.MarshalIndent(tasksCopy, "", "  ")
	if err != nil {
		return
	}
	_ = utils.AtomicWriteFile(tasksPath, data, 0644)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddDownloadTask 添加单个下载任务 (具备重复任务去重与隔离临时路径)
func (m *DownloadManager) AddDownloadTask(req *DownloadRequest, ep *bilibili.EpisodeInfo) (*DownloadTask, error) {
	m.mu.Lock()
	cfg := m.cfgMgr.Get()

	// 检查是否已有相同 CID + 画质 + 编码的活跃任务在排队、下载或合成中
	for _, existing := range m.tasks {
		if existing.CID == ep.CID &&
			existing.TargetQuality == req.TargetQuality &&
			existing.TargetCodec == req.TargetCodec &&
			(existing.Status == StatusQueued || existing.Status == StatusDownloading || existing.Status == StatusMerging) {
			m.mu.Unlock()
			return existing, nil
		}
	}

	// 生成规范的输出路径与文件名（支持极端特殊字符与超长标题安全降级）
	sanitizedTitle := utils.SanitizeFilename(req.Title, "bilibili_video")
	var outDir string
	var baseFileName string

	// 判断是否为多集/合集/多P视频
	isMulti := len(req.Episodes) > 1 || req.IsBangumi || strings.Contains(ep.Badge, "合集") || ep.Index > 1 || (ep.Title != "" && ep.Title != req.Title)

	if isMulti {
		// 合集/多P：保存在以合集标题 + [BVID] 命名的独立子文件夹内，彻底防止不同UP主同名合集相互覆盖
		collFolder := sanitizedTitle
		if ep.BVID != "" && !strings.Contains(collFolder, ep.BVID) {
			collFolder = fmt.Sprintf("%s [%s]", sanitizedTitle, ep.BVID)
		}
		outDir = filepath.Join(cfg.DownloadDir, collFolder)
		partName := formatFileNameByTemplate(cfg.FileNameTemplate, req.Title, ep.Title, ep.BVID, ep.Index)
		fallbackPart := fmt.Sprintf("P%02d", ep.Index)
		if ep.Index <= 0 {
			fallbackPart = "video"
		}
		baseFileName = utils.SanitizeFilename(partName, fallbackPart)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			// 如果因为异常路径或权限问题导致子文件夹创建失败，自动降级保存至主下载目录
			outDir = cfg.DownloadDir
		}
	} else {
		// 单视频：直接保存在下载主目录
		outDir = cfg.DownloadDir
		partName := formatFileNameByTemplate(cfg.FileNameTemplate, req.Title, ep.Title, ep.BVID, ep.Index)
		baseFileName = utils.SanitizeFilename(partName, sanitizedTitle)
		_ = os.MkdirAll(outDir, 0755)
	}

	// 确保在 Windows 260 字符限制下，完整输出路径保持在安全阈值内 (<= 240 字符)
	outPath := utils.EnsureSafePathLength(outDir, baseFileName, ".mp4")

	taskID := newUUID()
	shortID := taskID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	// 临时文件加入任务短 ID，保证多任务即使同名也 100% 物理隔离
	vTmp := filepath.Join(outDir, fmt.Sprintf("%s.%s.video.downloading", baseFileName, shortID))
	aTmp := filepath.Join(outDir, fmt.Sprintf("%s.%s.audio.downloading", baseFileName, shortID))

	task := &DownloadTask{
		ID:            taskID,
		BVID:          ep.BVID,
		AID:           ep.AID,
		CID:           ep.CID,
		EPID:          ep.EPID,
		IsBangumi:     req.IsBangumi,
		Title:         req.Title,
		PartTitle:     ep.Title,
		Cover:         ep.Cover,
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

	m.tasks = append([]*DownloadTask{task}, m.tasks...)
	m.mu.Unlock()

	m.SaveTasks()
	m.notifyChange(task)
	m.triggerSchedule()

	return task, nil
}

// PauseTask 暂停任务 (具备 Worker 取消安全)
func (m *DownloadManager) PauseTask(id string) error {
	m.mu.Lock()
	if w, ok := m.workers[id]; ok {
		w.cancel()
	}

	var task *DownloadTask
	for _, t := range m.tasks {
		if t.ID == id {
			task = t
			if t.Status == StatusDownloading || t.Status == StatusQueued {
				t.Status = StatusPaused
				t.Speed = 0
				t.SpeedStr = "0 KB/s"
				t.ETAStr = "--"
			}
			break
		}
	}
	m.mu.Unlock()

	if task != nil {
		m.SaveTasks()
		m.notifyChange(task)
		m.triggerSchedule()
	}
	return nil
}

// ResumeTask 恢复已暂停的任务
func (m *DownloadManager) ResumeTask(id string) error {
	m.mu.Lock()
	var task *DownloadTask
	for _, t := range m.tasks {
		if t.ID == id {
			task = t
			if t.Status == StatusPaused || t.Status == StatusError || t.Status == StatusCancelled {
				t.Status = StatusQueued
				t.ErrorMsg = ""
			}
			break
		}
	}
	m.mu.Unlock()

	if task != nil {
		m.SaveTasks()
		m.notifyChange(task)
		m.triggerSchedule()
	}
	return nil
}

// CancelTask 取消任务
func (m *DownloadManager) CancelTask(id string) error {
	m.mu.Lock()
	if w, ok := m.workers[id]; ok {
		w.cancel()
	}

	var task *DownloadTask
	for _, t := range m.tasks {
		if t.ID == id {
			task = t
			t.Status = StatusCancelled
			t.Speed = 0
			t.SpeedStr = "0 KB/s"
			break
		}
	}
	m.mu.Unlock()

	if task != nil {
		m.SaveTasks()
		m.notifyChange(task)
		m.triggerSchedule()
	}
	return nil
}

// DeleteTask 从列表中彻底删除任务
func (m *DownloadManager) DeleteTask(id string, deleteFile bool) error {
	m.mu.Lock()
	if w, ok := m.workers[id]; ok {
		w.cancel()
	}

	var newTasks []*DownloadTask
	var target *DownloadTask
	for _, t := range m.tasks {
		if t.ID == id {
			target = t
		} else {
			newTasks = append(newTasks, t)
		}
	}
	m.tasks = newTasks
	m.mu.Unlock()

	if target != nil {
		// 异步安全清理临时文件，带指数退避重试确保 Windows 句柄释放后成功清理 (杜绝 EACCES 权限泄漏)
		go func(vTmp, aTmp, outPath string, delFile bool) {
			safeRemoveWithRetry(vTmp)
			safeRemoveWithRetry(aTmp)
			if delFile && outPath != "" {
				safeRemoveWithRetry(outPath)
				// 如果所在目录是合集子目录且已经为空，顺便清理空文件夹 (严格排除主下载目录)
				dir := filepath.Dir(outPath)
				rootDir := filepath.Clean(m.cfgMgr.Get().DownloadDir)
				if dir != "" && dir != "." && filepath.Clean(dir) != rootDir {
					entries, err := os.ReadDir(dir)
					if err == nil && len(entries) == 0 {
						_ = os.Remove(dir)
					}
				}
			}
		}(target.VideoTmpPath, target.AudioTmpPath, target.OutputPath, deleteFile)
		m.SaveTasks()
		m.triggerSchedule()
	}
	return nil
}

// PauseAll 暂停所有正在下载或排队的任务 (无死锁安全实现)
func (m *DownloadManager) PauseAll() {
	m.mu.Lock()
	for _, w := range m.workers {
		w.cancel()
	}

	var modified []*DownloadTask
	for _, t := range m.tasks {
		if t.Status == StatusDownloading || t.Status == StatusQueued {
			t.Status = StatusPaused
			t.Speed = 0
			t.SpeedStr = "0 KB/s"
			t.ETAStr = "--"
			modified = append(modified, t)
		}
	}
	m.mu.Unlock()

	m.SaveTasks()
	for _, t := range modified {
		m.notifyChange(t)
	}
}

// ResumeAll 恢复所有已暂停的任务 (无死锁安全实现)
func (m *DownloadManager) ResumeAll() {
	m.mu.Lock()
	var modified []*DownloadTask
	for _, t := range m.tasks {
		if t.Status == StatusPaused || t.Status == StatusError {
			t.Status = StatusQueued
			t.ErrorMsg = ""
			modified = append(modified, t)
		}
	}
	m.mu.Unlock()

	m.SaveTasks()
	for _, t := range modified {
		m.notifyChange(t)
	}
	m.triggerSchedule()
}

// ClearCompleted 清理已完成的任务列表记录 (可选择是否同时删除本地文件)
func (m *DownloadManager) ClearCompleted(deleteFiles ...bool) {
	deleteFile := false
	if len(deleteFiles) > 0 {
		deleteFile = deleteFiles[0]
	}

	m.mu.Lock()
	var remaining []*DownloadTask
	var removed []*DownloadTask
	for _, t := range m.tasks {
		if t.Status != StatusCompleted && t.Status != StatusCancelled {
			remaining = append(remaining, t)
		} else {
			removed = append(removed, t)
		}
	}
	m.tasks = remaining
	m.mu.Unlock()

	if deleteFile {
		dirsToCheck := make(map[string]bool)
		for _, t := range removed {
			if t.OutputPath != "" {
				safeRemoveWithRetry(t.OutputPath)
				dir := filepath.Dir(t.OutputPath)
				if dir != "" && dir != "." {
					dirsToCheck[dir] = true
				}
			}
			safeRemoveWithRetry(t.VideoTmpPath)
			safeRemoveWithRetry(t.AudioTmpPath)
		}
		// 顺便清理可能残留的空合集目录 (严格排除主下载目录)
		rootDir := filepath.Clean(m.cfgMgr.Get().DownloadDir)
		for dir := range dirsToCheck {
			if filepath.Clean(dir) != rootDir {
				entries, err := os.ReadDir(dir)
				if err == nil && len(entries) == 0 {
					_ = os.Remove(dir)
				}
			}
		}
	}

	m.SaveTasks()
}

// GetTasks 获取当前所有任务的深拷贝快照 (彻底杜绝外部并发读写数据竞争)
func (m *DownloadManager) GetTasks() []*DownloadTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]*DownloadTask, len(m.tasks))
	for i, t := range m.tasks {
		tCopy := *t
		res[i] = &tCopy
	}
	return res
}

func (m *DownloadManager) triggerSchedule() {
	select {
	case m.workerNotify <- struct{}{}:
	default:
	}
}

func (m *DownloadManager) schedulerLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.workerNotify:
			m.checkAndSpawnTasks()
		case <-ticker.C:
			m.checkAndSpawnTasks()
		}
	}
}

func (m *DownloadManager) checkAndSpawnTasks() {
	m.mu.Lock()
	cfg := m.cfgMgr.Get()
	maxCon := cfg.MaxConcurrent
	if maxCon <= 0 {
		maxCon = 3
	}

	if len(m.workers) >= maxCon {
		m.mu.Unlock()
		return
	}

	var nextTask *DownloadTask
	for _, t := range m.tasks {
		if t.Status == StatusQueued {
			// 确保旧 worker 已彻底退出的任务才允许拉起新 worker
			if _, running := m.workers[t.ID]; !running {
				nextTask = t
				break
			}
		}
	}

	if nextTask == nil {
		m.mu.Unlock()
		return
	}

	nextTask.Status = StatusDownloading
	nextTask.ErrorMsg = ""

	token := newUUID()
	ctx, cancel := context.WithCancel(context.Background())
	m.workers[nextTask.ID] = workerHandle{token: token, cancel: cancel}
	m.mu.Unlock()

	m.notifyChange(nextTask)
	go m.runTask(ctx, nextTask, token)
}

func (m *DownloadManager) setTaskStatus(task *DownloadTask, status TaskStatus, errorMsg string) {
	m.mu.Lock()
	task.Status = status
	if errorMsg != "" {
		task.ErrorMsg = errorMsg
	}
	if status == StatusCompleted {
		task.CompletedAt = time.Now().Unix()
		task.Speed = 0
		task.SpeedStr = "已完成"
		task.ETAStr = "00:00"
		task.Progress = 100
	} else if status == StatusError {
		task.Speed = 0
		task.SpeedStr = "0 KB/s"
	} else if status == StatusMerging {
		task.Speed = 0
		task.SpeedStr = "合成中..."
		task.ETAStr = "处理中"
		task.Progress = 100
	}
	m.mu.Unlock()
	m.notifyChange(task)
}

func (m *DownloadManager) runTask(ctx context.Context, task *DownloadTask, token string) {
	defer func() {
		m.mu.Lock()
		// 校验 token：仅当当前任务句柄属于本 worker 时才清理，防止旧 worker 退出误删新 worker 的取消句柄
		if w, ok := m.workers[task.ID]; ok && w.token == token {
			delete(m.workers, task.ID)
		}
		m.mu.Unlock()
		m.SaveTasks()
		m.triggerSchedule()
	}()

	// 1. 获取最新媒体直链
	sel, err := m.biliClient.FetchStreamSelection(ctx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, task.TargetQuality, task.TargetCodec)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.setTaskStatus(task, StatusError, fmt.Sprintf("解析媒体流失败: %v", err))
		return
	}

	m.mu.Lock()
	task.QualityID = sel.QualityID
	task.QualityLabel = sel.QualityLabel
	task.Codec = sel.Codec

	// 提前计算并固化包含画质标签的 OutputPath，彻底消除下载期与合成期路径前后不一致
	qTag := getQualityTag(sel.QualityID, sel.QualityLabel)
	if qTag != "" {
		dir := filepath.Dir(task.OutputPath)
		base := strings.TrimSuffix(filepath.Base(task.OutputPath), ".mp4")
		if !strings.Contains(base, qTag) {
			task.OutputPath = utils.EnsureSafePathLength(dir, fmt.Sprintf("%s %s", base, qTag), ".mp4")
		}
	}
	m.mu.Unlock()
	m.SaveTasks()

	// 2. 准备视频与音频下载器 (支持候选 CDN 自动故障转移与 403 自动刷新换链)
	vDownloader := NewStreamDownloader(sel.VideoURLs, task.VideoTmpPath)
	vDownloader.SetURLRefresher(func(refCtx context.Context) ([]string, error) {
		newSel, rErr := m.biliClient.FetchStreamSelection(refCtx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, task.TargetQuality, task.TargetCodec)
		if rErr != nil {
			return nil, rErr
		}
		return newSel.VideoURLs, nil
	})
	vSize, _ := vDownloader.GetTotalSize(ctx)

	var aDownloader *StreamDownloader
	var aSize int64 = 0
	if len(sel.AudioURLs) > 0 && sel.AudioURLs[0] != "" {
		aDownloader = NewStreamDownloader(sel.AudioURLs, task.AudioTmpPath)
		aDownloader.SetURLRefresher(func(refCtx context.Context) ([]string, error) {
			newSel, rErr := m.biliClient.FetchStreamSelection(refCtx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, task.TargetQuality, task.TargetCodec)
			if rErr != nil {
				return nil, rErr
			}
			return newSel.AudioURLs, nil
		})
		aSize, _ = aDownloader.GetTotalSize(ctx)
	}

	totalBytes := vSize + aSize

	// 3. 统计已存在的部分大小
	var vDownloaded, aDownloaded int64
	if info, err := os.Stat(task.VideoTmpPath); err == nil {
		vDownloaded = info.Size()
	}
	if info, err := os.Stat(task.AudioTmpPath); err == nil {
		aDownloaded = info.Size()
	}

	m.mu.Lock()
	task.TotalBytes = totalBytes
	task.DownloadedBytes = vDownloaded + aDownloaded
	m.mu.Unlock()

	// 进度平滑计算
	lastTime := time.Now()
	var lastDownloaded = task.DownloadedBytes
	var currentSpeed int64 = 0

	progressTicker := time.NewTicker(250 * time.Millisecond)
	defer progressTicker.Stop()

	progressDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-progressDone:
				return
			case <-progressTicker.C:
				now := time.Now()
				durationSec := now.Sub(lastTime).Seconds()
				if durationSec > 0.1 {
					m.mu.RLock()
					currentDownloaded := task.DownloadedBytes
					total := task.TotalBytes
					m.mu.RUnlock()

					delta := currentDownloaded - lastDownloaded
					instantSpeed := int64(float64(delta) / durationSec)
					// 指数移动平均 (EMA) 平滑速度
					currentSpeed = int64(float64(currentSpeed)*0.7 + float64(instantSpeed)*0.3)
					speedStr := utils.FormatSpeed(currentSpeed)

					var progress float64
					var sizeStr, etaStr string
					if total > 0 {
						progress = float64(currentDownloaded) * 100.0 / float64(total)
						if progress > 100 {
							progress = 100
						}
						sizeStr = fmt.Sprintf("%s / %s", utils.FormatBytes(currentDownloaded), utils.FormatBytes(total))

						if currentSpeed > 0 {
							remainBytes := total - currentDownloaded
							if remainBytes > 0 {
								sec := remainBytes / currentSpeed
								etaStr = utils.FormatDuration(int(sec))
							} else {
								etaStr = "完成中"
							}
						}
					} else {
						sizeStr = utils.FormatBytes(currentDownloaded)
					}

					m.mu.Lock()
					task.Speed = currentSpeed
					task.SpeedStr = speedStr
					task.Progress = progress
					task.SizeStr = sizeStr
					task.ETAStr = etaStr
					m.mu.Unlock()

					lastTime = now
					lastDownloaded = currentDownloaded
					m.notifyChange(task)
				}
			}
		}
	}()

	cfg := m.cfgMgr.Get()
	threads := cfg.ThreadsPerTask
	if threads <= 0 {
		threads = 4
	}
	if threads > 8 {
		threads = 8
	}

	// 4. 下载视频轨 (大体积媒体流，使用多线程分片加速，带自动降级保护)
	err = vDownloader.DownloadWithConcurrency(ctx, threads, func(delta int64) {
		m.mu.Lock()
		task.DownloadedBytes += delta
		if task.DownloadedBytes < 0 {
			task.DownloadedBytes = 0
		}
		m.mu.Unlock()
	})
	if err != nil {
		close(progressDone)
		if ctx.Err() != nil {
			return
		}
		m.setTaskStatus(task, StatusError, formatFriendlyError(err))
		return
	}

	// 5. 下载音频轨 (小体积媒体流，使用单流平稳下载)
	if aDownloader != nil {
		err = aDownloader.DownloadSingleStream(ctx, func(delta int64) {
			m.mu.Lock()
			task.DownloadedBytes += delta
			if task.DownloadedBytes < 0 {
				task.DownloadedBytes = 0
			}
			m.mu.Unlock()
		})
		if err != nil {
			close(progressDone)
			if ctx.Err() != nil {
				return
			}
			m.setTaskStatus(task, StatusError, formatFriendlyError(err))
			return
		}
	}
	close(progressDone)

	// 6. 合成音视频
	m.setTaskStatus(task, StatusMerging, "")

	// 为最终输出文件确认路径
	m.mu.RLock()
	finalOutPath := task.OutputPath
	vTmpP := task.VideoTmpPath
	aTmpP := task.AudioTmpPath
	m.mu.RUnlock()

	// 6. 原生纯 Go 极速无损音视频复用合成 (0 依赖，毫秒级完成)
	err = MergeAudioVideo(vTmpP, aTmpP, finalOutPath, cfg.DeleteTempFiles)
	if err != nil {
		m.setTaskStatus(task, StatusError, fmt.Sprintf("音视频合成失败: %v", err))
		return
	}

	// 确保临时 downloading 文件在任何平台都被彻底清理干净
	if cfg.DeleteTempFiles {
		_ = os.Remove(vTmpP)
		if aTmpP != "" {
			_ = os.Remove(aTmpP)
		}
	}

	// 7. 标记任务完成
	m.setTaskStatus(task, StatusCompleted, "")
}

func getQualityTag(qn int, label string) string {
	switch qn {
	case 127:
		return "[8K]"
	case 126:
		return "[杜比视界]"
	case 125:
		return "[HDR]"
	case 120:
		return "[4K]"
	case 116:
		return "[1080P60]"
	case 112:
		return "[1080P+]"
	case 80:
		return "[1080P]"
	case 74:
		return "[720P60]"
	case 64:
		return "[720P]"
	case 32:
		return "[480P]"
	case 16:
		return "[360P]"
	default:
		if label != "" {
			if strings.Contains(label, "8K") {
				return "[8K]"
			} else if strings.Contains(label, "4K") {
				return "[4K]"
			} else if strings.Contains(label, "1080P60") {
				return "[1080P60]"
			} else if strings.Contains(label, "1080P") {
				return "[1080P]"
			} else if strings.Contains(label, "720P") {
				return "[720P]"
			} else if strings.Contains(label, "480P") {
				return "[480P]"
			} else if strings.Contains(label, "360P") {
				return "[360P]"
			}
		}
		return ""
	}
}

func cleanPartTitle(mainTitle, partTitle string, index int) string {
	cleanMain := strings.TrimSpace(mainTitle)
	cleanPart := strings.TrimSpace(partTitle)

	// 如果分集标题带有冗余的合集主标题前缀，去除前缀
	if strings.HasPrefix(cleanPart, cleanMain) {
		cleanPart = strings.TrimPrefix(cleanPart, cleanMain)
		cleanPart = strings.TrimLeft(cleanPart, " -_—|:")
	}

	if cleanPart == "" {
		if index > 0 {
			return fmt.Sprintf("P%02d", index)
		}
		return cleanMain
	}

	if index > 0 && !strings.HasPrefix(cleanPart, fmt.Sprintf("P%d", index)) &&
		!strings.HasPrefix(cleanPart, fmt.Sprintf("P%02d", index)) &&
		!strings.HasPrefix(cleanPart, fmt.Sprintf("第%d", index)) {
		return fmt.Sprintf("P%02d. %s", index, cleanPart)
	}

	return cleanPart
}

// formatFileNameByTemplate 根据用户自定义模板格式化生成文件名
func formatFileNameByTemplate(tmpl, mainTitle, partTitle, bvid string, index int) string {
	cleanMain := strings.TrimSpace(mainTitle)
	cleanPart := strings.TrimSpace(partTitle)

	if strings.HasPrefix(cleanPart, cleanMain) {
		cleanPart = strings.TrimPrefix(cleanPart, cleanMain)
		cleanPart = strings.TrimLeft(cleanPart, " -_—|:")
	}

	// 1. 如果是单视频，part 与 main 相同或为空，直接返回主标题（或自定义模板）
	if cleanPart == "" || cleanPart == cleanMain {
		tmplTrim := strings.TrimSpace(tmpl)
		if tmplTrim == "" || tmplTrim == "{title} - {part}" {
			return cleanMain
		}
		res := strings.ReplaceAll(tmplTrim, "{title} - {part}", "{title}")
		res = strings.ReplaceAll(res, "{part} - {title}", "{title}")
		res = strings.ReplaceAll(res, "{part}", cleanMain)
		res = strings.ReplaceAll(res, "{title}", cleanMain)
		res = strings.ReplaceAll(res, "{bvid}", bvid)
		res = strings.ReplaceAll(res, "{index}", "")
		res = strings.Trim(strings.TrimSpace(res), " ._-")
		if res == "" {
			return cleanMain
		}
		return res
	}

	// 2. 如果是多集视频且为默认模板 "{title} - {part}"（或空）：使用标准的规范序号命名 (如 P01. 分P标题)
	tmplTrim := strings.TrimSpace(tmpl)
	if tmplTrim == "" || tmplTrim == "{title} - {part}" {
		return cleanPartTitle(cleanMain, cleanPart, index)
	}

	// 3. 用户显式自定义了模板：严格按自定义占位符替换
	idxStr := ""
	if index > 0 {
		idxStr = fmt.Sprintf("P%02d", index)
	}

	res := tmplTrim
	res = strings.ReplaceAll(res, "{title}", cleanMain)
	res = strings.ReplaceAll(res, "{part}", cleanPart)
	res = strings.ReplaceAll(res, "{index}", idxStr)
	res = strings.ReplaceAll(res, "{bvid}", bvid)

	res = strings.Trim(strings.TrimSpace(res), " ._-")
	if res == "" {
		return cleanPart
	}
	return res
}

// safeRemoveWithRetry 带指数退避重试的文件安全删除函数 (转调 utils.SafeRemoveWithRetry)
func safeRemoveWithRetry(filePath string) {
	utils.SafeRemoveWithRetry(filePath)
}

func formatFriendlyError(err error) string {
	if err == nil {
		return ""
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded") {
		return "网络连接超时，请点击右侧重试按钮继续下载"
	}
	if strings.Contains(errStr, "connection reset") || strings.Contains(errStr, "eof") || strings.Contains(errStr, "broken pipe") {
		return "网络连接中断，请点击重试恢复下载"
	}
	if strings.Contains(errStr, "403") {
		return "下载直链已过期，请点击重试自动刷新"
	}
	if strings.Contains(errStr, "no space") || strings.Contains(errStr, "disk full") {
		return "磁盘空间不足，请清理后重试"
	}
	return "下载异常中断，请点击右侧重试按钮恢复"
}
