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

// DownloadManager 负责整个下载队列、并发调度与状态管理
type DownloadManager struct {
	mu           sync.RWMutex
	tasks        []*DownloadTask
	activeCount  int
	cancelFuncs  map[string]context.CancelFunc
	callback     ProgressCallback
	biliClient   *bilibili.Client
	cfgMgr       *config.ConfigManager
	workerNotify chan struct{}
}

var (
	managerInstance *DownloadManager
	managerOnce     sync.Once
)

// GetManager 获取全局下载管理器单例
func GetManager() *DownloadManager {
	managerOnce.Do(func() {
		m := &DownloadManager{
			tasks:        make([]*DownloadTask, 0),
			cancelFuncs:  make(map[string]context.CancelFunc),
			biliClient:   bilibili.GetDefaultClient(),
			cfgMgr:       config.GetManager(),
			workerNotify: make(chan struct{}, 50),
		}
		m.loadTasks()
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
	m.mu.RLock()
	cb := m.callback
	m.mu.RUnlock()
	if cb != nil && t != nil {
		// 传递浅拷贝避免并发冲突
		tCopy := *t
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

// SaveTasks 持久化任务列表
func (m *DownloadManager) SaveTasks() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tasksPath := m.cfgMgr.GetTasksPath()
	data, err := json.MarshalIndent(m.tasks, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(tasksPath, data, 0644)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddDownloadTask 添加单个下载任务
func (m *DownloadManager) AddDownloadTask(req *DownloadRequest, ep *bilibili.EpisodeInfo) (*DownloadTask, error) {
	m.mu.Lock()
	cfg := m.cfgMgr.Get()

	// 生成规范的输出路径与文件名
	sanitizedTitle := utils.SanitizeFilename(req.Title)
	var outDir string
	var baseFileName string

	// 判断是否为多集/合集/多P视频
	isMulti := len(req.Episodes) > 1 || req.IsBangumi || strings.Contains(ep.Badge, "合集") || ep.Index > 1 || (ep.Title != "" && ep.Title != req.Title)

	if isMulti {
		// 合集/多P：保存在以合集标题命名的独立子文件夹内
		outDir = filepath.Join(cfg.DownloadDir, sanitizedTitle)
		partName := cleanPartTitle(req.Title, ep.Title, ep.Index)
		baseFileName = utils.SanitizeFilename(partName)
	} else {
		// 单视频：直接保存在下载主目录
		outDir = cfg.DownloadDir
		baseFileName = sanitizedTitle
	}

	_ = os.MkdirAll(outDir, 0755)

	outPath := filepath.Join(outDir, baseFileName+".mp4")

	taskID := newUUID()
	vTmp := filepath.Join(outDir, fmt.Sprintf("%s.video.downloading", baseFileName))
	aTmp := filepath.Join(outDir, fmt.Sprintf("%s.audio.downloading", baseFileName))

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

// PauseTask 暂停任务
func (m *DownloadManager) PauseTask(id string) error {
	m.mu.Lock()
	cancel, ok := m.cancelFuncs[id]
	if ok {
		cancel()
		delete(m.cancelFuncs, id)
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
	if cancel, ok := m.cancelFuncs[id]; ok {
		cancel()
		delete(m.cancelFuncs, id)
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
	if cancel, ok := m.cancelFuncs[id]; ok {
		cancel()
		delete(m.cancelFuncs, id)
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
		_ = os.Remove(target.VideoTmpPath)
		_ = os.Remove(target.AudioTmpPath)
		if deleteFile && target.OutputPath != "" {
			_ = os.Remove(target.OutputPath)
		}
		m.SaveTasks()
		m.triggerSchedule()
	}
	return nil
}

// PauseAll 暂停所有正在下载或排队的任务
func (m *DownloadManager) PauseAll() {
	m.mu.Lock()
	for _, cancel := range m.cancelFuncs {
		cancel()
	}
	m.cancelFuncs = make(map[string]context.CancelFunc)

	for _, t := range m.tasks {
		if t.Status == StatusDownloading || t.Status == StatusQueued {
			t.Status = StatusPaused
			t.Speed = 0
			t.SpeedStr = "0 KB/s"
			t.ETAStr = "--"
			m.notifyChange(t)
		}
	}
	m.mu.Unlock()
	m.SaveTasks()
}

// ResumeAll 恢复所有已暂停的任务
func (m *DownloadManager) ResumeAll() {
	m.mu.Lock()
	for _, t := range m.tasks {
		if t.Status == StatusPaused || t.Status == StatusError {
			t.Status = StatusQueued
			t.ErrorMsg = ""
			m.notifyChange(t)
		}
	}
	m.mu.Unlock()
	m.SaveTasks()
	m.triggerSchedule()
}

// ClearCompleted 清理已完成的任务列表记录
func (m *DownloadManager) ClearCompleted() {
	m.mu.Lock()
	var remaining []*DownloadTask
	for _, t := range m.tasks {
		if t.Status != StatusCompleted && t.Status != StatusCancelled {
			remaining = append(remaining, t)
		}
	}
	m.tasks = remaining
	m.mu.Unlock()
	m.SaveTasks()
}

// GetTasks 获取当前所有任务
func (m *DownloadManager) GetTasks() []*DownloadTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]*DownloadTask, len(m.tasks))
	copy(res, m.tasks)
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

	if m.activeCount >= maxCon {
		m.mu.Unlock()
		return
	}

	var nextTask *DownloadTask
	for _, t := range m.tasks {
		if t.Status == StatusQueued {
			nextTask = t
			break
		}
	}

	if nextTask == nil {
		m.mu.Unlock()
		return
	}

	nextTask.Status = StatusDownloading
	nextTask.ErrorMsg = ""
	m.activeCount++

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFuncs[nextTask.ID] = cancel
	m.mu.Unlock()

	m.notifyChange(nextTask)
	go m.runTask(ctx, nextTask)
}

func (m *DownloadManager) runTask(ctx context.Context, task *DownloadTask) {
	defer func() {
		m.mu.Lock()
		m.activeCount--
		delete(m.cancelFuncs, task.ID)
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
		task.Status = StatusError
		task.ErrorMsg = fmt.Sprintf("解析媒体流失败: %v", err)
		m.notifyChange(task)
		return
	}

	task.QualityID = sel.QualityID
	task.QualityLabel = sel.QualityLabel
	task.Codec = sel.Codec

	// 2. 准备视频与音频下载器
	vDownloader := NewStreamDownloader(sel.VideoURL, task.VideoTmpPath)
	vSize, _ := vDownloader.GetTotalSize(ctx)

	var aDownloader *StreamDownloader
	var aSize int64 = 0
	if sel.AudioURL != "" {
		aDownloader = NewStreamDownloader(sel.AudioURL, task.AudioTmpPath)
		aSize, _ = aDownloader.GetTotalSize(ctx)
	}

	totalBytes := vSize + aSize
	task.TotalBytes = totalBytes

	// 3. 统计已存在的部分大小
	var vDownloaded, aDownloaded int64
	if info, err := os.Stat(task.VideoTmpPath); err == nil {
		vDownloaded = info.Size()
	}
	if info, err := os.Stat(task.AudioTmpPath); err == nil {
		aDownloaded = info.Size()
	}

	task.DownloadedBytes = vDownloaded + aDownloaded

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
					delta := task.DownloadedBytes - lastDownloaded
					instantSpeed := int64(float64(delta) / durationSec)
					// 指数移动平均 (EMA) 平滑速度
					currentSpeed = int64(float64(currentSpeed)*0.7 + float64(instantSpeed)*0.3)
					task.Speed = currentSpeed
					task.SpeedStr = utils.FormatSpeed(currentSpeed)

					if task.TotalBytes > 0 {
						task.Progress = float64(task.DownloadedBytes) * 100.0 / float64(task.TotalBytes)
						if task.Progress > 100 {
							task.Progress = 100
						}
						task.SizeStr = fmt.Sprintf("%s / %s", utils.FormatBytes(task.DownloadedBytes), utils.FormatBytes(task.TotalBytes))

						if currentSpeed > 0 {
							remainBytes := task.TotalBytes - task.DownloadedBytes
							if remainBytes > 0 {
								sec := remainBytes / currentSpeed
								task.ETAStr = utils.FormatDuration(int(sec))
							} else {
								task.ETAStr = "完成中"
							}
						}
					} else {
						task.SizeStr = utils.FormatBytes(task.DownloadedBytes)
					}

					lastTime = now
					lastDownloaded = task.DownloadedBytes
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
		task.DownloadedBytes += delta
	})
	if err != nil {
		close(progressDone)
		if ctx.Err() != nil {
			return
		}
		task.Status = StatusError
		task.ErrorMsg = formatFriendlyError(err)
		m.notifyChange(task)
		return
	}

	// 5. 下载音频轨 (小体积媒体流，使用单流平稳下载)
	if aDownloader != nil {
		err = aDownloader.DownloadSingleStream(ctx, func(delta int64) {
			task.DownloadedBytes += delta
		})
		if err != nil {
			close(progressDone)
			if ctx.Err() != nil {
				return
			}
			task.Status = StatusError
			task.ErrorMsg = formatFriendlyError(err)
			m.notifyChange(task)
			return
		}
	}
	close(progressDone)

	// 6. 合成音视频
	task.Status = StatusMerging
	task.Speed = 0
	task.SpeedStr = "合成中..."
	task.ETAStr = "处理中"
	task.Progress = 100
	m.notifyChange(task)

	ffmpegPath := cfg.FFmpegPath
	if ffmpegPath == "" || !fileExists(ffmpegPath) {
		ffmpegPath = config.DetectFFmpeg()
	}
	if ffmpegPath == "" && task.AudioTmpPath != "" && fileExists(task.AudioTmpPath) {
		task.SpeedStr = "部署合成引擎中..."
		m.notifyChange(task)
		if fp, err := EnsureFFmpeg(ctx, func(msg string) {
			task.SpeedStr = msg
			m.notifyChange(task)
		}); err == nil && fp != "" {
			ffmpegPath = fp
		}
	}

	// 为最终输出文件名附加 [清晰度] 标识（如 [1080P]、[4K]）
	qTag := getQualityTag(task.QualityID, task.QualityLabel)
	if qTag != "" {
		dir := filepath.Dir(task.OutputPath)
		base := strings.TrimSuffix(filepath.Base(task.OutputPath), ".mp4")
		if !strings.Contains(base, qTag) {
			task.OutputPath = filepath.Join(dir, fmt.Sprintf("%s %s.mp4", base, qTag))
		}
	}

	err = MergeAudioVideo(ffmpegPath, task.VideoTmpPath, task.AudioTmpPath, task.OutputPath, cfg.DeleteTempFiles)
	if err != nil {
		task.Status = StatusError
		task.ErrorMsg = fmt.Sprintf("音视频合成失败: %v", err)
		m.notifyChange(task)
		return
	}

	// 7. 标记任务完成
	task.Status = StatusCompleted
	task.CompletedAt = time.Now().Unix()
	task.Speed = 0
	task.SpeedStr = "已完成"
	task.ETAStr = "00:00"
	m.notifyChange(task)
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
