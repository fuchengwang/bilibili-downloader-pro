package downloader

// TaskStatus 任务当前状态
type TaskStatus string

const (
	StatusQueued      TaskStatus = "queued"      // 排队中
	StatusDownloading TaskStatus = "downloading" // 下载中
	StatusPaused      TaskStatus = "paused"      // 已暂停
	StatusMerging     TaskStatus = "merging"     // 合成中
	StatusCompleted   TaskStatus = "completed"   // 已完成
	StatusError       TaskStatus = "error"       // 错误
	StatusCancelled   TaskStatus = "cancelled"   // 已取消
)

// DownloadTask 表示一个完整的单集/分P下载任务
type DownloadTask struct {
	ID              string     `json:"id"`              // 唯一 UUID
	BVID            string     `json:"bvid"`            // 视频 BVID
	AID             int64      `json:"aid"`             // AID
	CID             int64      `json:"cid"`             // CID
	EPID            int64      `json:"epid"`            // 番剧 EPID
	IsBangumi       bool       `json:"isBangumi"`       // 是否为番剧
	Title           string     `json:"title"`           // 视频主标题
	PartTitle       string     `json:"partTitle"`       // 分P/分集标题
	Cover           string     `json:"cover"`           // 封面图 URL
	TargetQuality   string     `json:"targetQuality"`   // 目标画质 (e.g. "highest", "120", "80")
	TargetCodec     string     `json:"targetCodec"`     // 目标编码 (e.g. "auto", "AVC", "HEVC", "AV1")
	QualityID       int        `json:"qualityId"`       // 最终解析出的清晰度 ID
	QualityLabel    string     `json:"qualityLabel"`    // 最终清晰度名称 (如 "1080P 高清")
	Codec           string     `json:"codec"`           // 最终编码名
	Status          TaskStatus `json:"status"`          // 当前状态
	Progress        float64    `json:"progress"`        // 总体进度 0.0 ~ 100.0
	Speed           int64      `json:"speed"`           // 当前实时速度 (B/s)
	SpeedStr        string     `json:"speedStr"`        // 格式化速度 (e.g. "12.4 MB/s")
	DownloadedBytes int64      `json:"downloadedBytes"` // 已下载总字节数 (视频+音频)
	TotalBytes      int64      `json:"totalBytes"`      // 总字节数 (视频+音频)
	SizeStr         string     `json:"sizeStr"`         // 格式化大小 (e.g. "120 MB / 340 MB")
	ETAStr          string     `json:"etaStr"`          // 预估剩余时间 (e.g. "12s")
	Duration        int        `json:"duration"`        // 视频时长(秒)
	ErrorMsg        string     `json:"errorMsg"`        // 错误信息
	CreatedAt       int64      `json:"createdAt"`       // 创建时间戳
	CompletedAt     int64      `json:"completedAt"`     // 完成时间戳
	OutputPath      string     `json:"outputPath"`      // 最终合并输出的文件绝对路径
	VideoTmpPath    string     `json:"videoTmpPath"`    // 视频临时分块文件路径
	AudioTmpPath    string     `json:"audioTmpPath"`    // 音频临时分块文件路径
}

// DownloadRequest 前端发起的下载请求结构体
type DownloadRequest struct {
	BVID          string   `json:"bvid"`
	AID           int64    `json:"aid"`
	Title         string   `json:"title"`
	Cover         string   `json:"cover"`
	IsBangumi     bool     `json:"isBangumi"`
	TargetQuality string   `json:"targetQuality"` // 用户选定的画质 ("highest", "120", "80", etc.)
	TargetCodec   string   `json:"targetCodec"`   // 用户选定的编码 ("auto", "AVC", "HEVC", "AV1")
	Episodes      []int64  `json:"episodes"`      // 选中的集数 CID 列表
}
