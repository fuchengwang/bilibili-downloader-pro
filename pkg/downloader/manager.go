package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/utils"
)

// ProgressCallback 进度通知函数
type ProgressCallback func(t *DownloadTask)

type workerHandle struct {
	token         string
	cancel        context.CancelFunc
	invalidated   bool
	skipFinalSave bool // a queue transaction already persisted this worker's final task state
	done          <-chan struct{}
}

// DownloadManager 负责整个下载队列、并发调度与状态管理
type DownloadManager struct {
	trace             downloadTrace
	mu                sync.RWMutex
	addMu             sync.Mutex
	directoryMu       sync.Mutex
	directoryRegistry map[string]string
	directoryWritable bool
	saveMu            sync.Mutex
	tasks             []*DownloadTask
	workers           map[string]workerHandle
	retiringPaths     map[string]int // reserved until background storage cleanup finishes
	callback          ProgressCallback
	batchCallback     func([]*DownloadTask)
	biliClient        *bilibili.Client
	cfgMgr            *config.ConfigManager
	workerNotify      chan struct{}
	mergeFunc         func(context.Context, string, string, string, bool) error
	bandwidth         *bandwidthLimiter
	streamFactory     func([]string, string) *StreamDownloader // isolated transport/acceptance tests
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
	m.bandwidth = &bandwidthLimiter{limit: func() int64 { return cfgMgr.Get().DownloadLimitKB * 1024 }}
	if m.loadTasks() {
		m.reconcileTaskStorage()
	}
	return m
}

// GetManager 获取全局下载管理器单例
func GetManager() *DownloadManager {
	managerOnce.Do(func() {
		m := NewDownloadManager(config.GetManager())
		if downloadTraceEnabled {
			m.trace = startDownloadTrace(filepath.Dir(m.cfgMgr.GetTasksPath()))
		}
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

func (m *DownloadManager) SetBatchCallback(cb func([]*DownloadTask)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchCallback = cb
}

func (m *DownloadManager) notifyTaskBatch(tasks []*DownloadTask) {
	if len(tasks) == 0 {
		return
	}
	m.mu.RLock()
	cb := m.batchCallback
	if cb == nil {
		m.mu.RUnlock()
		for _, task := range tasks {
			m.notifyChange(task)
		}
		return
	}
	present := make(map[*DownloadTask]bool, len(m.tasks))
	for _, task := range m.tasks {
		present[task] = true
	}
	snapshots := make([]*DownloadTask, 0, len(tasks))
	for _, task := range tasks {
		if present[task] {
			snapshot := *task
			snapshots = append(snapshots, &snapshot)
		}
	}
	m.mu.RUnlock()
	if len(snapshots) > 0 {
		cb(snapshots)
	}
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

func (m *DownloadManager) loadTasks() bool {
	tasksPath := m.cfgMgr.GetTasksPath()
	data, err := os.ReadFile(tasksPath)
	if err != nil {
		return os.IsNotExist(err)
	}
	var loaded []*DownloadTask
	if err := json.Unmarshal(data, &loaded); err != nil {
		return false
	}
	validTasks := make([]*DownloadTask, 0, len(loaded))
	for _, t := range loaded {
		if t == nil {
			continue
		}
		// Continue the prior running queue; an explicit user pause stays paused.
		if t.Status == StatusDownloading || t.Status == StatusMerging || (t.Status == StatusPaused && t.ResumeOnStartup) {
			t.Status = StatusQueued
			t.Speed = 0
			t.SpeedStr = "0 KB/s"
			t.ETAStr = "--"
		}
		t.ResumeOnStartup = false
		validTasks = append(validTasks, t)
	}
	m.tasks = validTasks
	assignMissingQueueOrders(m.tasks)
	return true
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

	return m.writeTaskSnapshot(tasksCopy)
}

func (m *DownloadManager) writeTaskSnapshot(tasksCopy []*DownloadTask) error {
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

// AddDownloadTask uses the same transaction as a batch, keeping duplicate-task
// and cache reuse behavior consistent for internal callers.
func (m *DownloadManager) AddDownloadTask(req *DownloadRequest, ep *bilibili.EpisodeInfo) (*DownloadTask, error) {
	if ep == nil {
		return nil, fmt.Errorf("下载任务参数不能为空")
	}
	tasks, err := m.AddDownloadTasks(req, []bilibili.EpisodeInfo{*ep})
	if err != nil {
		return nil, err
	}
	return tasks[0], nil
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
			t.ResumeOnStartup = false
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

// ResumeTask requeues a paused/failed task at its original persisted position.
func (m *DownloadManager) ResumeTask(id string) error { return m.resumeTasks(id, false) }

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
			t.ResumeOnStartup = false
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

// RemoveCompletedRecords removes matching completed records without touching files.
func (m *DownloadManager) RemoveCompletedRecords(candidates map[string]string) ([]string, error) {
	if len(candidates) == 0 {
		return []string{}, nil
	}
	m.mu.Lock()
	var kept, removed []*DownloadTask
	for _, task := range m.tasks {
		if task == nil {
			continue
		}
		path, matches := candidates[task.ID]
		if matches && task.Status == StatusCompleted && task.OutputPath == path {
			if _, working := m.workers[task.ID]; !working {
				removed = append(removed, task)
				continue
			}
		}
		kept = append(kept, task)
	}
	m.tasks = kept
	m.mu.Unlock()
	if len(removed) == 0 {
		return []string{}, nil
	}
	if err := m.SaveTasks(); err != nil {
		m.mu.Lock()
		m.tasks = append(m.tasks, removed...)
		m.mu.Unlock()
		return nil, err
	}
	ids := make([]string, 0, len(removed))
	for _, task := range removed {
		ids = append(ids, task.ID)
	}
	return ids, nil
}

// DeleteTask 从列表中彻底删除任务
func (m *DownloadManager) DeleteTask(id string, deleteFile bool) error {
	cleanup, err := m.removeTasksForCleanup(func(task *DownloadTask) bool { return task.ID == id }, deleteFile)
	if err != nil || len(cleanup) == 0 {
		return err
	}
	go func() {
		var directories []*DownloadTask
		for i := range cleanup {
			task := &cleanup[i]
			m.cleanReservedTaskFiles(task)
			if deleteFile || task.Status != StatusCompleted {
				directories = append(directories, &task.DownloadTask)
			}
		}
		if len(directories) > 0 {
			m.cleanupEmptyCollectionDirs(directories)
		}
	}()
	m.triggerSchedule()
	return nil
}

// PauseAll 暂停所有正在下载或排队的任务 (无死锁安全实现)
func (m *DownloadManager) PauseAll() error {
	modified, err := func() ([]*DownloadTask, error) {
		m.saveMu.Lock()
		defer m.saveMu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		var changed []*DownloadTask
		for _, task := range m.tasks {
			if task == nil {
				continue
			}
			canPause := task.Status == StatusDownloading || task.Status == StatusQueued || task.Status == StatusMerging
			if !canPause && !task.ResumeOnStartup {
				continue
			}
			changed = append(changed, task)
			task.ResumeOnStartup = false
			if canPause {
				task.Status = StatusPaused
				task.Speed, task.SpeedStr, task.ETAStr = 0, "0 KB/s", "--"
			}
		}
		if len(changed) == 0 {
			return nil, nil
		}
		for id, worker := range m.workers {
			worker.invalidated = true
			m.workers[id] = worker
			if worker.cancel != nil {
				worker.cancel()
			}
		}
		err := m.writeTaskSnapshot(m.tasks)
		for id, worker := range m.workers {
			worker.skipFinalSave = err == nil
			m.workers[id] = worker
		}
		return changed, err
	}()
	m.notifyTaskBatch(modified)
	return err
}

// ResumeAll resumes paused and failed tasks without reordering the queue.
func (m *DownloadManager) ResumeAll() error { return m.resumeTasks("", true) }

// ClearCompleted 清理已完成的任务列表记录 (可选择是否同时删除本地文件)
func (m *DownloadManager) ClearCompleted(deleteFiles ...bool) error {
	deleteFile := len(deleteFiles) > 0 && deleteFiles[0]
	cleanup, err := m.removeTasksForCleanup(func(task *DownloadTask) bool {
		return task.Status == StatusCompleted || task.Status == StatusCancelled
	}, deleteFile)
	if err != nil || len(cleanup) == 0 {
		return err
	}
	var directories []*DownloadTask
	for i := range cleanup {
		task := &cleanup[i]
		m.cleanReservedTaskFiles(task)
		if deleteFile {
			directories = append(directories, &task.DownloadTask)
		}
	}
	if len(directories) > 0 {
		m.cleanupEmptyCollectionDirs(directories)
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

func (m *DownloadManager) activeWorkerCountLocked() int {
	count := 0
	for _, w := range m.workers {
		if !w.invalidated {
			count++
		}
	}
	return count
}

func (m *DownloadManager) checkAndSpawnTasks() {
	m.mu.Lock()
	cfg := m.cfgMgr.Get()
	maxCon := cfg.MaxConcurrent
	if maxCon <= 0 {
		maxCon = 3
	}

	freeSlots := maxCon - m.activeWorkerCountLocked()
	if freeSlots <= 0 {
		m.mu.Unlock()
		return
	}

	type start struct {
		task  *DownloadTask
		token string
		ctx   context.Context
		done  chan struct{}
	}
	var queued []*DownloadTask
	for _, task := range m.tasks {
		if task != nil && task.Status == StatusQueued {
			queued = append(queued, task)
		}
	}
	sort.SliceStable(queued, func(i, j int) bool {
		return queued[i].QueueOrder < queued[j].QueueOrder
	})

	var starts []start
	for _, nextTask := range queued[:min(freeSlots, len(queued))] {
		// Reserve this slot until the cancelled worker releases its files.
		if _, running := m.workers[nextTask.ID]; running {
			continue
		}
		nextTask.Status, nextTask.ErrorMsg = StatusDownloading, ""
		token := newUUID()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		m.workers[nextTask.ID] = workerHandle{token: token, cancel: cancel, done: done}
		starts = append(starts, start{nextTask, token, ctx, done})
	}
	m.mu.Unlock()

	for _, s := range starts {
		m.notifyChange(s.task)
		go func() {
			defer close(s.done)
			m.runTask(s.ctx, s.task, s.token)
		}()
	}
}

func (m *DownloadManager) setTaskStatus(task *DownloadTask, status TaskStatus, errorMsg string) {
	m.mu.Lock()
	m.applyTaskStatusLocked(task, status, errorMsg)
	m.mu.Unlock()
	m.notifyChange(task)
}

func (m *DownloadManager) applyTaskStatusLocked(task *DownloadTask, status TaskStatus, errorMsg string) {
	task.Status = status
	task.ErrorKind, task.ErrorHint, task.ErrorDetail = "", "", ""
	if status != StatusError {
		task.ErrorMsg = ""
	}
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

func (m *DownloadManager) setWorkerTaskStatus(task *DownloadTask, token string, expected, status TaskStatus, errorMsg string, playbackErrors ...*bilibili.PlaybackError) bool {
	m.mu.Lock()
	w, owned := m.workers[task.ID]
	if !owned || w.invalidated || w.token != token || (expected != "" && task.Status != expected) {
		m.mu.Unlock()
		return false
	}
	m.applyTaskStatusLocked(task, status, errorMsg)
	if status == StatusError && len(playbackErrors) > 0 && playbackErrors[0] != nil {
		info := playbackErrors[0]
		task.ErrorKind, task.ErrorHint, task.ErrorDetail = info.Kind, info.Hint, info.Detail
	}
	m.mu.Unlock()
	m.notifyChange(task)
	return true
}

func (m *DownloadManager) runTask(ctx context.Context, task *DownloadTask, token string) {
	var trace downloadTrace
	if downloadTraceEnabled && m.trace.active() {
		trace = m.trace.forTask(task, token)
		cfg := m.cfgMgr.Get()
		trace.event("task_start", map[string]any{
			"target_quality": task.TargetQuality, "target_codec": task.TargetCodec,
			"concurrent_tasks": cfg.MaxConcurrent, "threads_per_track": cfg.ThreadsPerTask,
			"limit_kib_per_second": cfg.DownloadLimitKB,
		})
	}
	defer func() {
		m.mu.Lock()
		// 校验 token：仅当当前任务句柄属于本 worker 时才清理，防止旧 worker 退出误删新 worker 的取消句柄
		saveTasks := true
		if w, ok := m.workers[task.ID]; ok && w.token == token {
			saveTasks = !w.skipFinalSave
			delete(m.workers, task.ID)
		}
		var status TaskStatus
		var downloaded, total int64
		if downloadTraceEnabled && trace.active() {
			status, downloaded, total = task.Status, task.DownloadedBytes, task.TotalBytes
		}
		m.mu.Unlock()
		if downloadTraceEnabled && trace.active() {
			trace.event("task_end", map[string]any{
				"status": status, "downloaded_bytes": downloaded, "total_bytes": total,
				"context_error": traceError(ctx.Err()),
			})
		}
		if saveTasks {
			m.persistTasks()
		}
		m.triggerSchedule()
	}()

	// 1. 获取最新媒体直链
	sel, err := m.biliClient.FetchStreamSelection(ctx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, task.TargetQuality, task.TargetCodec, task.IsCheese)
	if downloadTraceEnabled && trace.active() {
		fields := map[string]any{"error": traceError(err)}
		if sel != nil {
			fields["quality_id"], fields["codec"] = sel.QualityID, sel.Codec
			fields["video_sources"], fields["audio_sources"] = traceSources(sel.VideoURLs), traceSources(sel.AudioURLs)
			fields["video_size_hint"], fields["audio_size_hint"] = sel.VideoSize, sel.AudioSize
		}
		trace.event("playback_selection", fields)
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		info := bilibili.PlaybackErrorInfo(err)
		m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, info.Message, info)
		return
	}
	courseKeys, err := m.biliClient.ResolveCourseKeys(ctx, sel)
	if err != nil {
		if downloadTraceEnabled && trace.active() {
			trace.event("course_authorization_error", map[string]any{"error": traceError(err)})
		}
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
		if downloadTraceEnabled && trace.active() {
			trace.event("output_path_error", map[string]any{"error": traceError(pathErr)})
		}
		message := pathErr.Error()
		if isDiskSpaceError(pathErr) {
			message = diskSpaceMessage
		}
		m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, message)
		return
	}
	if err := m.relocateTaskStreams(ctx, task); err != nil {
		if downloadTraceEnabled && trace.active() {
			trace.event("stream_storage_error", map[string]any{"error": traceError(err)})
		}
		if ctx.Err() == nil {
			m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, fileFailureMessage("准备下载中文件失败", err))
		}
		return
	}

	// Media transport is independent of the Bilibili parser and uses one
	// coalesced refresh for the exact representation selected for this task.
	refresh := playbackRefresher(sel, func(refCtx context.Context) (*bilibili.StreamSelection, error) {
		return m.biliClient.FetchStreamSelection(refCtx, task.BVID, task.AID, task.CID, task.EPID, task.IsBangumi, strconv.Itoa(sel.QualityID), sel.Codec, task.IsCheese)
	})
	factory := m.streamFactory
	if factory == nil {
		factory = NewStreamDownloader
	}
	vDownloader := factory(sel.VideoURLs, task.VideoTmpPath)
	if downloadTraceEnabled {
		vDownloader.trace = trace.forTrack("video")
	}
	vDownloader.checkpoints = m.checkpoints()
	vDownloader.representation = mediaRepresentationKey(sel.VideoURLs)
	vDownloader.sizeHint = sel.VideoSize
	vDownloader.skipSizeProbe = true
	vDownloader.bandwidth = m.bandwidth
	vDownloader.SetURLRefresher(func(refCtx context.Context) ([]string, error) {
		fresh, err := refresh(refCtx)
		if err != nil {
			return nil, err
		}
		return fresh.VideoURLs, nil
	})
	var aDownloader *StreamDownloader
	if len(filterStreamURLs(sel.AudioURLs)) > 0 {
		aDownloader = factory(sel.AudioURLs, task.AudioTmpPath)
		if downloadTraceEnabled {
			aDownloader.trace = trace.forTrack("audio")
		}
		aDownloader.checkpoints = m.checkpoints()
		aDownloader.representation = mediaRepresentationKey(sel.AudioURLs)
		aDownloader.sizeHint = sel.AudioSize
		aDownloader.skipSizeProbe = true
		aDownloader.bandwidth = m.bandwidth
		aDownloader.SetURLRefresher(func(refCtx context.Context) ([]string, error) {
			fresh, err := refresh(refCtx)
			if err != nil {
				return nil, err
			}
			return fresh.AudioURLs, nil
		})
	}

	// 3. Keep restored disk bytes separate from this run's network traffic.
	progress := newTaskDownloadProgress(ctx, m, task, token, 0, 0, aDownloader != nil)
	progress.setEstimatedSizes(sel.VideoSize, sel.AudioSize)
	vDownloader.SetRestoredProgressCallback(func(delta int64) { progress.record(delta, false) })
	vDownloader.SetTotalSizeCallback(func(size int64) { progress.setSize(true, size) })
	if aDownloader != nil {
		aDownloader.SetRestoredProgressCallback(func(delta int64) { progress.record(delta, false) })
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

	// Both media headers contribute to accurate progress from startup.
	if failure := downloadMediaTracks(ctx, vDownloader, aDownloader, threads, func(delta int64) { progress.record(delta, true) }); failure != nil {
		if ctx.Err() != nil {
			return
		}
		stopProgress()
		progress.tick(time.Now())
		m.setWorkerTaskStatus(task, token, StatusDownloading, StatusError, formatFriendlyError(failure.err), &bilibili.PlaybackError{Detail: streamFailureDetail(failure.label, failure.err, failure.stream.lastDiagnostic())})
		return
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
				m.setWorkerTaskStatus(task, token, StatusMerging, StatusError, fileFailureMessage("课堂音视频处理失败", processErr))
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
	if downloadTraceEnabled && trace.active() {
		trace.event("merge_result", map[string]any{"error": traceError(err)})
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.setWorkerTaskStatus(task, token, StatusMerging, StatusError, fileFailureMessage("音视频合成失败", err))
		return
	}

	// 确保临时 downloading 文件在任何平台都被彻底清理干净
	if cfg.DeleteTempFiles {
		m.checkpoints().cleanupStream(task.VideoTmpPath)
		m.checkpoints().cleanupStream(aTmpP)
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

var diagnosticURLPattern = regexp.MustCompile(`https?://[^\s\"']+`)

func streamFailureDetail(stage string, err, last error) string {
	detail := stage + "下载失败: " + err.Error()
	if last != nil && last.Error() != err.Error() {
		detail += "; 最近一次请求: " + last.Error()
	}
	return diagnosticURLPattern.ReplaceAllString(detail, "[下载地址已隐藏]")
}

func formatFriendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errEntityChanged) || errors.Is(err, errRangeUnsupported) {
		return "下载节点返回的媒体数据不一致，请点击重试"
	}
	var requestErr *url.Error
	if errors.As(err, &requestErr) && requestErr.Err != nil {
		err = requestErr.Err
	}
	if isDiskSpaceError(err) {
		return diskSpaceMessage
	}
	var status *mediaHTTPError
	if errors.As(err, &status) {
		if isMediaAuthStatus(status.code) {
			return "下载地址已过期，请点击重试重新获取"
		}
		switch status.code {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed,
			http.StatusRequestedRangeNotSatisfiable, http.StatusTooManyRequests,
			http.StatusUnavailableForLegalReasons, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return "视频服务器暂时繁忙，请稍后点击重试"
		}
	}
	errStr := strings.ToLower(err.Error())
	if errors.Is(err, os.ErrPermission) ||
		strings.Contains(errStr, "permission denied") ||
		strings.Contains(errStr, "access is denied") ||
		strings.Contains(errStr, "read-only file system") {
		return "目标文件夹没有写入权限，请检查保存路径后重试"
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) ||
		strings.Contains(errStr, "保存媒体数据失败") ||
		strings.Contains(errStr, "no such file or directory") ||
		strings.Contains(errStr, "input/output error") {
		return "无法写入下载文件，请检查保存目录或磁盘状态后重试"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "host is unreachable") ||
		strings.Contains(errStr, "proxyconnect") ||
		strings.Contains(errStr, "x509:") ||
		strings.Contains(errStr, "tls:") {
		return "无法连接网络，请检查网络连接或代理设置后重试"
	}
	if errors.Is(err, errIdleStream) ||
		errors.Is(err, errSlowStream) ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "超过读取等待时间") ||
		strings.Contains(errStr, "持续低速") {
		return "网络连接超时或过慢，请检查网络后点击重试继续下载"
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "eof") ||
		strings.Contains(errStr, "broken pipe") {
		return "网络连接中断，请点击重试继续下载"
	}
	if errors.Is(err, errNonMediaResponse) || strings.Contains(errStr, "媒体响应类型异常") {
		return "下载节点返回的内容无法验证，请稍后点击重试"
	}
	if errors.Is(err, errAllSourcesRejected) ||
		strings.Contains(errStr, "不支持的内容编码") ||
		strings.Contains(errStr, "媒体范围或内容反复变化") ||
		strings.Contains(errStr, "刷新后的媒体轨道发生变化") {
		return "视频服务器暂时繁忙，请稍后点击重试"
	}
	return "下载意外中断，请检查网络后点击重试"
}
