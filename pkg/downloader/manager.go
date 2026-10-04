package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
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
	token       string
	cancel      context.CancelFunc
	invalidated bool
	done        <-chan struct{}
}

// DownloadManager 负责整个下载队列、并发调度与状态管理
type DownloadManager struct {
	mu           sync.RWMutex
	saveMu       sync.Mutex
	tasks        []*DownloadTask
	workers      map[string]workerHandle
	callback     ProgressCallback
	biliClient   *bilibili.Client
	cfgMgr       *config.ConfigManager
	workerNotify chan struct{}
	mergeFunc    func(context.Context, string, string, string, bool) error
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
		mergeFunc:    MergeAudioVideoContext,
	}
	m.loadTasks()
	m.reconcileTaskStorage()
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
	present := false
	for _, existing := range m.tasks {
		if existing == t {
			present = true
			break
		}
	}
	m.mu.RUnlock()
	if cb != nil && present {
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
		validTasks := make([]*DownloadTask, 0, len(loaded))
		for _, t := range loaded {
			if t == nil {
				continue
			}
			// 将未完成的任务重置为暂停或队列状态
			if t.Status == StatusDownloading || t.Status == StatusMerging {
				t.Status = StatusPaused
				t.Speed = 0
				t.SpeedStr = "0 KB/s"
			}
			validTasks = append(validTasks, t)
		}
		m.tasks = validTasks
	}
}

// SaveTasks 持久化任务列表 (原子写入避免崩溃产生损坏文件，杜绝 Windows 权限冲突)
func (m *DownloadManager) SaveTasks() error {
	// 先串行化“取快照 + 写文件”，避免旧快照在新快照之后完成写入，导致重启后回退到旧状态。
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.mu.RLock()
	tasksCopy := make([]*DownloadTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
		tCopy := *t
		tasksCopy = append(tasksCopy, &tCopy)
	}
	m.mu.RUnlock()

	tasksPath := m.cfgMgr.GetTasksPath()
	data, err := json.MarshalIndent(tasksCopy, "", "  ")
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(tasksPath, data, 0644)
}

func (m *DownloadManager) persistTasks() {
	if err := m.SaveTasks(); err != nil {
		log.Printf("保存下载任务列表失败: %v", err)
	}
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddDownloadTask 添加单个下载任务 (具备重复任务去重与隔离临时路径)
func (m *DownloadManager) AddDownloadTask(req *DownloadRequest, ep *bilibili.EpisodeInfo) (*DownloadTask, error) {
	if req == nil || ep == nil {
		return nil, fmt.Errorf("下载任务参数不能为空")
	}
	m.mu.Lock()
	cfg := m.cfgMgr.Get()
	if strings.TrimSpace(cfg.DownloadDir) == "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("下载目录未设置")
	}
	if err := os.MkdirAll(cfg.DownloadDir, 0755); err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("创建下载目录失败: %w", err)
	}

	// 相同参数复用原任务；完成文件仍存在时不重复下载。
	for _, existing := range m.tasks {
		if existing == nil {
			continue
		}
		if existing.CID == ep.CID &&
			existing.IsCheese == req.IsCheese &&
			existing.IsBangumi == req.IsBangumi &&
			existing.TargetQuality == req.TargetQuality &&
			existing.TargetCodec == req.TargetCodec {
			if existing.Status == StatusCompleted {
				// Changing the destination is an explicit request for another copy.
				if !pathWithin(cfg.DownloadDir, existing.OutputPath) {
					continue
				}
				info, statErr := os.Stat(existing.OutputPath)
				if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
					m.mu.Unlock()
					return existing, nil
				}
				if statErr != nil && !os.IsNotExist(statErr) {
					m.mu.Unlock()
					return nil, fmt.Errorf("检查已下载文件失败: %w", statErr)
				}
				before := *existing
				existing.Status = StatusQueued
				existing.CompletedAt = 0
				existing.Progress = 0
				existing.DownloadedBytes = 0
				existing.Speed = 0
				existing.SpeedStr = "0 KB/s"
				existing.SizeStr = "准备中..."
				existing.ErrorMsg = ""
				m.mu.Unlock()
				if err := m.SaveTasks(); err != nil {
					m.mu.Lock()
					*existing = before
					m.mu.Unlock()
					return nil, err
				}
				m.notifyChange(existing)
				m.triggerSchedule()
				return existing, nil
			}
			resume := existing.Status == StatusPaused || existing.Status == StatusError || existing.Status == StatusCancelled
			m.mu.Unlock()
			if resume {
				if err := m.ResumeTask(existing.ID); err != nil {
					return nil, err
				}
			}
			return existing, nil
		}
	}

	// 生成规范的输出路径与文件名（支持极端特殊字符与超长标题安全降级）
	sanitizedTitle := utils.SanitizeFilename(req.Title, "bilibili_video")
	var outDir string
	var baseFileName string

	// 判断是否为多集/合集/多P视频
	isMulti := len(req.Episodes) > 1 || req.IsBangumi || req.IsCheese || strings.Contains(ep.Badge, "合集") || ep.Index > 1 || (ep.Title != "" && ep.Title != req.Title)

	if isMulti {
		// 合集/多P：保存在以合集标题 + [BVID] 命名的独立子文件夹内，彻底防止不同UP主同名合集相互覆盖
		collFolder := sanitizedTitle
		if req.IsCheese && req.SSID > 0 {
			collFolder = fmt.Sprintf("%s [ss%d]", sanitizedTitle, req.SSID)
		} else if ep.BVID != "" && !strings.Contains(collFolder, ep.BVID) {
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
			m.mu.Unlock()
			return nil, fmt.Errorf("创建合集下载目录失败: %w", err)
		}
	} else {
		// 单视频：直接保存在下载主目录
		outDir = cfg.DownloadDir
		partName := formatFileNameByTemplate(cfg.FileNameTemplate, req.Title, ep.Title, ep.BVID, ep.Index)
		baseFileName = utils.SanitizeFilename(partName, sanitizedTitle)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("创建下载目录失败: %w", err)
		}
	}

	// 确保在 Windows 260 字符限制下，完整输出路径保持在安全阈值内 (<= 240 字符)
	outPath := utils.EnsureSafePathLength(outDir, baseFileName, ".mp4")

	taskID := newUUID()
	// 每个任务独占缓存子目录，下载目录只保存成品。
	vTmp, aTmp := m.cachePaths(taskID)

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

	persistErr := m.SaveTasks()

	if persistErr != nil {
		// Save failure must not leave an in-memory task which the caller was told
		// was not accepted and which cannot be recovered after restart.
		m.mu.Lock()
		for i, existing := range m.tasks {
			if existing == task {
				m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		return nil, fmt.Errorf("保存下载任务失败: %w", persistErr)
	}
	m.notifyChange(task)
	m.triggerSchedule()
	return task, nil
}

// PauseTask 暂停任务 (具备 Worker 取消安全)
func (m *DownloadManager) PauseTask(id string) error {
	m.mu.Lock()
	if w, ok := m.workers[id]; ok {
		if w.cancel != nil {
			w.cancel()
		}
		w.invalidated = true
		m.workers[id] = w
	}

	var task *DownloadTask
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
		if t.ID == id {
			task = t
			if t.Status == StatusDownloading || t.Status == StatusQueued || t.Status == StatusMerging {
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
		persistErr := m.SaveTasks()
		m.notifyChange(task)
		m.triggerSchedule()
		return persistErr
	}
	return nil
}

// ResumeTask 恢复已暂停的任务
func (m *DownloadManager) ResumeTask(id string) error {
	m.mu.Lock()
	var task *DownloadTask
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
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
		persistErr := m.SaveTasks()
		m.notifyChange(task)
		m.triggerSchedule()
		return persistErr
	}
	return nil
}

// CancelTask 取消任务
func (m *DownloadManager) CancelTask(id string) error {
	m.mu.Lock()
	if w, ok := m.workers[id]; ok {
		if w.cancel != nil {
			w.cancel()
		}
		w.invalidated = true
		m.workers[id] = w
	}

	var task *DownloadTask
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
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
		persistErr := m.SaveTasks()
		m.notifyChange(task)
		m.triggerSchedule()
		return persistErr
	}
	return nil
}

// DeleteTask 从列表中彻底删除任务
func (m *DownloadManager) DeleteTask(id string, deleteFile bool) error {
	m.mu.Lock()
	var workerDone <-chan struct{}
	if w, ok := m.workers[id]; ok {
		workerDone = w.done
		if w.cancel != nil {
			w.cancel()
		}
		w.invalidated = true
		m.workers[id] = w
	}

	var newTasks []*DownloadTask
	var target *DownloadTask
	targetIndex := -1
	for i, t := range m.tasks {
		if t.ID == id {
			target = t
			targetIndex = i
		} else {
			newTasks = append(newTasks, t)
		}
	}
	m.tasks = newTasks
	m.mu.Unlock()

	if target != nil {
		persistErr := m.SaveTasks()
		if persistErr != nil {
			// 持久化失败时恢复内存中的任务，避免前端已经看到错误但当前进程
			// 却把任务删掉；文件清理也必须延后到持久化成功之后。
			m.mu.Lock()
			present := false
			for _, existing := range m.tasks {
				if existing != nil && existing.ID == target.ID {
					present = true
					break
				}
			}
			if !present {
				idx := targetIndex
				if idx < 0 || idx > len(m.tasks) {
					idx = len(m.tasks)
				}
				m.tasks = append(m.tasks, nil)
				copy(m.tasks[idx+1:], m.tasks[idx:])
				m.tasks[idx] = target
			}
			if target.Status == StatusDownloading || target.Status == StatusMerging {
				target.Status = StatusPaused
				target.Speed = 0
				target.SpeedStr = "0 KB/s"
				target.ETAStr = "--"
			}
			m.mu.Unlock()
			return persistErr
		}

		// 异步安全清理临时文件，带指数退避重试确保 Windows 句柄释放后成功清理 (杜绝 EACCES 权限泄漏)
		go func(vTmp, aTmp, outPath string, delFile bool) {
			if workerDone != nil {
				<-workerDone
			}
			cleanupStreamArtifacts(vTmp)
			cleanupStreamArtifacts(aTmp)
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
		m.triggerSchedule()
		return persistErr
	}
	return nil
}

// PauseAll 暂停所有正在下载或排队的任务 (无死锁安全实现)
func (m *DownloadManager) PauseAll() error {
	m.mu.Lock()
	for _, w := range m.workers {
		if w.cancel != nil {
			w.cancel()
		}
		w.invalidated = true
	}
	for id, w := range m.workers {
		m.workers[id] = w
	}

	var modified []*DownloadTask
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
		if t.Status == StatusDownloading || t.Status == StatusQueued || t.Status == StatusMerging {
			t.Status = StatusPaused
			t.Speed = 0
			t.SpeedStr = "0 KB/s"
			t.ETAStr = "--"
			modified = append(modified, t)
		}
	}
	m.mu.Unlock()

	persistErr := m.SaveTasks()
	for _, t := range modified {
		m.notifyChange(t)
	}
	return persistErr
}

// ResumeAll 恢复所有已暂停的任务 (无死锁安全实现)
func (m *DownloadManager) ResumeAll() error {
	m.mu.Lock()
	var modified []*DownloadTask
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
		if t.Status == StatusPaused || t.Status == StatusError {
			t.Status = StatusQueued
			t.ErrorMsg = ""
			modified = append(modified, t)
		}
	}
	m.mu.Unlock()

	persistErr := m.SaveTasks()
	for _, t := range modified {
		m.notifyChange(t)
	}
	m.triggerSchedule()
	return persistErr
}

// ClearCompleted 清理已完成的任务列表记录 (可选择是否同时删除本地文件)
func (m *DownloadManager) ClearCompleted(deleteFiles ...bool) error {
	deleteFile := false
	if len(deleteFiles) > 0 {
		deleteFile = deleteFiles[0]
	}

	m.mu.Lock()
	var remaining []*DownloadTask
	var removed []*DownloadTask
	workerDone := make(map[string]<-chan struct{})
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
		if t.Status != StatusCompleted && t.Status != StatusCancelled {
			remaining = append(remaining, t)
		} else {
			removed = append(removed, t)
			if w, ok := m.workers[t.ID]; ok {
				workerDone[t.ID] = w.done
				if w.cancel != nil {
					w.cancel()
				}
				w.invalidated = true
				m.workers[t.ID] = w
			}
		}
	}
	m.tasks = remaining
	m.mu.Unlock()

	// 文件删除必须在任务列表成功落盘后进行；否则持久化失败时重启会
	// 重新加载指向已被删除文件的“已完成”记录，造成不可逆的数据丢失。
	persistErr := m.SaveTasks()
	if persistErr != nil {
		m.mu.Lock()
		for _, removedTask := range removed {
			present := false
			for _, existing := range m.tasks {
				if existing != nil && existing.ID == removedTask.ID {
					present = true
					break
				}
			}
			if present {
				continue
			}
			m.tasks = append([]*DownloadTask{removedTask}, m.tasks...)
			if removedTask.Status == StatusDownloading || removedTask.Status == StatusMerging {
				removedTask.Status = StatusPaused
				removedTask.Speed = 0
				removedTask.SpeedStr = "0 KB/s"
				removedTask.ETAStr = "--"
			}
		}
		m.mu.Unlock()
		return persistErr
	}

	for _, t := range removed {
		if done := workerDone[t.ID]; done != nil {
			<-done
		}
		cleanupStreamArtifacts(t.VideoTmpPath)
		cleanupStreamArtifacts(t.AudioTmpPath)
	}
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

	return nil
}

// GetTasks 获取当前所有任务的深拷贝快照 (彻底杜绝外部并发读写数据竞争)
func (m *DownloadManager) GetTasks() []*DownloadTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]*DownloadTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t == nil {
			continue
		}
		tCopy := *t
		res = append(res, &tCopy)
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
		if t == nil {
			continue
		}
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
	done := make(chan struct{})
	m.workers[nextTask.ID] = workerHandle{token: token, cancel: cancel, done: done}
	m.mu.Unlock()

	m.notifyChange(nextTask)
	go func() {
		defer close(done)
		m.runTask(ctx, nextTask, token)
	}()
}

func (m *DownloadManager) setTaskStatus(task *DownloadTask, status TaskStatus, errorMsg string) {
	m.mu.Lock()
	m.applyTaskStatusLocked(task, status, errorMsg)
	m.mu.Unlock()
	m.notifyChange(task)
}

func (m *DownloadManager) applyTaskStatusLocked(task *DownloadTask, status TaskStatus, errorMsg string) {
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
}

func (m *DownloadManager) setWorkerTaskStatus(task *DownloadTask, token string, expected, status TaskStatus, errorMsg string) bool {
	m.mu.Lock()
	w, owned := m.workers[task.ID]
	if !owned || w.invalidated || w.token != token || (expected != "" && task.Status != expected) {
		m.mu.Unlock()
		return false
	}
	m.applyTaskStatusLocked(task, status, errorMsg)
	m.mu.Unlock()
	m.notifyChange(task)
	return true
}

func (m *DownloadManager) runTask(ctx context.Context, task *DownloadTask, token string) {
	defer func() {
		m.mu.Lock()
		// 校验 token：仅当当前任务句柄属于本 worker 时才清理，防止旧 worker 退出误删新 worker 的取消句柄
		if w, ok := m.workers[task.ID]; ok && w.token == token {
			delete(m.workers, task.ID)
		}
		m.mu.Unlock()
		m.persistTasks()
		m.triggerSchedule()
	}()

	// 1. 获取最新媒体直链
	sel, err := m.biliClient.FetchStreamSelection(ctx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, task.TargetQuality, task.TargetCodec, task.IsCheese)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, fmt.Sprintf("解析媒体流失败: %v", err))
		return
	}
	courseKeys, err := m.biliClient.ResolveCourseKeys(ctx, sel)
	if err != nil {
		if ctx.Err() == nil {
			m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, fmt.Sprintf("课堂播放授权失败: %v", err))
		}
		return
	}
	defer func() {
		for _, key := range courseKeys {
			clear(key)
		}
	}()

	m.mu.Lock()
	worker, owned := m.workers[task.ID]
	if !owned || worker.invalidated || worker.token != token || task.Status != StatusDownloading {
		m.mu.Unlock()
		return
	}
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
	outputPath, pathErr := m.availableOutputPathLocked(task.OutputPath, task.ID)
	if pathErr == nil {
		task.OutputPath = outputPath
	}
	m.mu.Unlock()
	if pathErr != nil {
		m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, pathErr.Error())
		return
	}
	m.persistTasks()

	// 2. 准备视频与音频下载器 (支持候选 CDN 自动故障转移与 403 自动刷新换链)
	vDownloader := NewStreamDownloader(sel.VideoURLs, task.VideoTmpPath)
	vDownloader.SetURLRefresher(func(refCtx context.Context) ([]string, error) {
		newSel, rErr := m.biliClient.FetchStreamSelection(refCtx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, task.TargetQuality, task.TargetCodec, task.IsCheese)
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
			newSel, rErr := m.biliClient.FetchStreamSelection(refCtx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, task.TargetQuality, task.TargetCodec, task.IsCheese)
			if rErr != nil {
				return nil, rErr
			}
			return newSel.AudioURLs, nil
		})
		aSize, _ = aDownloader.GetTotalSize(ctx)
	}

	// 3. Keep restored disk bytes separate from this run's network traffic.
	progress := newTaskDownloadProgress(ctx, m, task, token, vSize, aSize, aDownloader != nil)
	vDownloader.SetRestoredProgressCallback(func(delta int64) { progress.record(delta, false) })
	vDownloader.SetTotalSizeCallback(func(size int64) { progress.setSize(true, size) })
	if aDownloader != nil {
		aDownloader.SetTotalSizeCallback(func(size int64) { progress.setSize(false, size) })
	}
	stopProgress := progress.start()
	defer stopProgress()

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
		progress.record(delta, true)
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, formatFriendlyError(err))
		return
	}

	// 5. 下载音频轨 (小体积媒体流，使用单流平稳下载)
	if aDownloader != nil {
		err = aDownloader.DownloadSingleStream(ctx, func(delta int64) {
			progress.record(delta, true)
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, formatFriendlyError(err))
			return
		}
	}
	// 6. 合成音视频
	stopProgress()
	if !m.setWorkerTaskStatus(task, token, StatusDownloading, StatusMerging, "") {
		return
	}

	// 为最终输出文件确认路径
	m.mu.RLock()
	finalOutPath := task.OutputPath
	vTmpP := task.VideoTmpPath
	aTmpP := task.AudioTmpPath
	audioMergePath := aTmpP
	if aDownloader == nil {
		audioMergePath = ""
	}
	m.mu.RUnlock()
	if task.IsCheese {
		clearVideo, processErr := prepareCourseStreamContext(ctx, vTmpP, courseKeys)
		if processErr == nil {
			if clearVideo != vTmpP {
				defer os.Remove(clearVideo)
			}
			vTmpP = clearVideo
			if audioMergePath != "" {
				clearAudio, audioErr := prepareCourseStreamContext(ctx, audioMergePath, courseKeys)
				processErr = audioErr
				if audioErr == nil {
					if clearAudio != audioMergePath {
						defer os.Remove(clearAudio)
					}
					audioMergePath = clearAudio
				}
			}
		}
		if processErr != nil {
			if ctx.Err() == nil {
				m.setWorkerTaskStatus(task, token, StatusMerging, StatusError, fmt.Sprintf("课堂音视频处理失败: %v", processErr))
			}
			return
		}
	}

	// 6. 原生纯 Go 极速无损音视频复用合成 (0 依赖，毫秒级完成)
	mergeFunc := m.mergeFunc
	if mergeFunc == nil {
		mergeFunc = MergeAudioVideoContext
	}
	err = mergeFunc(ctx, vTmpP, audioMergePath, finalOutPath, cfg.DeleteTempFiles)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.setWorkerTaskStatus(task, token, StatusMerging, StatusError, fmt.Sprintf("音视频合成失败: %v", err))
		return
	}

	// 确保临时 downloading 文件在任何平台都被彻底清理干净
	if cfg.DeleteTempFiles {
		cleanupStreamArtifacts(task.VideoTmpPath)
		cleanupStreamArtifacts(aTmpP)
	}

	// 7. 标记任务完成
	m.setWorkerTaskStatus(task, token, StatusMerging, StatusCompleted, "")
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
