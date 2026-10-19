package sonarr

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"arr-download-proxy/internal/config"
	"arr-download-proxy/internal/logsafe"
)

type Client struct {
	cfg        config.SonarrConfig
	httpClient *http.Client
}

func NewClient(cfg config.SonarrConfig) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		},
	}
}

// Sonarr /api/v3/history 响应结构体
type HistoryResponse struct {
	Records []HistoryRecord `json:"records"`
}

type HistoryRecord struct {
	EventType   string      `json:"eventType"`
	DownloadId  string      `json:"downloadId"`
	SourceTitle string      `json:"sourceTitle"`
	Series      *SeriesInfo `json:"series"`
}

type SeriesInfo struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

// Sonarr Webhook (On Grab) Payload 结构体
type GrabWebhookPayload struct {
	EventType      string      `json:"eventType"`
	DownloadId     string      `json:"downloadId"`
	DownloadClient string      `json:"downloadClient"`
	Series         *SeriesInfo `json:"series"`
}

// 通过 InfoHash 轮询查询 Sonarr 的 Grab 记录
// 由于 Sonarr 发起添加请求后，需要极短时间发布事件并写入数据库，因此这里内置重试机制
func (c *Client) QuerySeriesPathWithRetry(infoHash string, maxRetries int, interval time.Duration) (seriesTitle, seriesPath string, err error) {
	lowerHash := strings.ToLower(infoHash)
	var lastErr error
	confirmedMissing := false

	for i := 0; i < maxRetries; i++ {
		title, path, found, queryErr := c.queryHistoryByHash(lowerHash)
		if queryErr == nil && found {
			log.Printf("[Sonarr] 来源=api hash=%s 第%d/%d次 命中 目标=%s", lowerHash, i+1, maxRetries, path)
			return title, path, nil
		}
		if queryErr != nil {
			lastErr = queryErr
			confirmedMissing = false
			log.Printf("[Sonarr] 来源=api hash=%s 第%d/%d次 失败=%s 等待=%s", lowerHash, i+1, maxRetries, logsafe.Error(queryErr), interval)
		} else {
			confirmedMissing = true
			log.Printf("[Sonarr] 来源=api hash=%s 第%d/%d次 未命中 等待=%s", lowerHash, i+1, maxRetries, interval)
		}
		// 每次重试等待指定间隔，包括最后一次，保持原有时序。
		time.Sleep(interval)
	}

	if lastErr != nil && !confirmedMissing {
		return "", "", fmt.Errorf("Sonarr API 失败，不能判断 Hash %s 是否已有抓取记录: %w", infoHash, lastErr)
	}
	return "", "", fmt.Errorf("在 Sonarr 中未找到 Hash 为 %s 的抓取记录", infoHash)
}

// 内部查询 /api/v3/history
func (c *Client) queryHistoryByHash(targetHash string) (title, path string, found bool, err error) {
	// eventType 查询参数是整数枚举，grabbed=1。字符串 grabbed 会被 Sonarr 判为 400。
	// 默认 history 不带 series，必须 includeSeries 才能拿到路径。
	url := fmt.Sprintf("%s/api/v3/history?eventType=1&pageSize=20&includeSeries=true", strings.TrimRight(c.cfg.BaseURL, "/"))
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "", false, err
	}

	// 填入 Sonarr API Key
	req.Header.Set("X-Api-Key", c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("调用 Sonarr API 失败: %s", logsafe.Redact(err.Error()))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", false, fmt.Errorf("Sonarr 返回非 200 状态码 (%d): %s", resp.StatusCode, logsafe.Redact(string(body)))
	}

	var history HistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		return "", "", false, fmt.Errorf("解析 Sonarr 响应失败: %w", err)
	}

	for _, item := range history.Records {
		if strings.EqualFold(item.DownloadId, targetHash) {
			if item.Series != nil && item.Series.Path != "" {
				return item.Series.Title, item.Series.Path, true, nil
			}
			log.Printf("[Sonarr] 来源=api hash=%s 命中记录但没有 Series.Path", targetHash)
			return "", "", false, nil
		}
	}

	return "", "", false, nil
}
