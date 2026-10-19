package model

import "time"

// 任务生命周期状态定义
type TaskState string

const (
	// 已接收到种子，但尚未匹配到目标剧集路径
	StateReceived TaskState = "RECEIVED"
	// 已匹配到目标路径与下载路径，准备推给下载器
	StateReady TaskState = "READY"
	// 已成功推给 BiglyBT 开始下载
	StateDownloading TaskState = "DOWNLOADING"
	// 下载已完成，等待或正在由 Sonarr 进行 Hardlink 导入
	StateCompleted TaskState = "COMPLETED"
	// 失败状态
	StateFailed TaskState = "FAILED"
)

// 下载任务模型，以 InfoHash 为唯一主键
type DownloadTask struct {
	// 40位全小写十六进制 InfoHash，天然幂等键
	InfoHash string `json:"info_hash"`
	// 种子显示名称
	Name string `json:"name"`
	// 原始种子数据（base64 或二进制），用于意外崩溃后重放投递
	RawTorrent []byte `json:"-"`
	// 磁力链接（若是以 URL/Magnet 方式添加）
	MagnetURL string `json:"magnet_url,omitempty"`
	// 当前任务状态
	State TaskState `json:"state"`
	// 关联的剧集名称
	SeriesTitle string `json:"series_title,omitempty"`
	// 目标剧集根目录 B（例如 /mnt/disk1/Media/TV/Breaking Bad）
	DestSeriesPath string `json:"dest_series_path,omitempty"`
	// 实际物理下载目录 A（例如 /mnt/disk1/Downloads/incomplete）
	ActualDownloadDir string `json:"actual_download_dir,omitempty"`
	// 伪装给 Sonarr Docker 容器内部的路径（例如 /downloads/incomplete）
	DockerMappedDir string `json:"docker_mapped_dir,omitempty"`
	// 来源分类（Category / Tag）
	Category string `json:"category,omitempty"`
	// 错误描述信息
	ErrorMessage string `json:"error_message,omitempty"`
	// 创建时间
	CreatedAt time.Time `json:"created_at"`
	// 更新时间
	UpdatedAt time.Time `json:"updated_at"`
}
