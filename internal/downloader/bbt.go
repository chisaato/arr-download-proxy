package downloader

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"arr-download-proxy/internal/config"
	"arr-download-proxy/internal/logsafe"
)

type BiglyBTClient struct {
	cfg        config.BiglyBTConfig
	httpClient *http.Client
	sessionID  string
	mu         sync.Mutex
}

func NewBiglyBTClient(cfg config.BiglyBTConfig) *BiglyBTClient {
	return &BiglyBTClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Transmission RPC 请求通用结构
type RPCRequest struct {
	Method    string                 `json:"method"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
	Tag       int                    `json:"tag,omitempty"`
}

// Transmission RPC 响应通用结构
type RPCResponse struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Tag       int             `json:"tag,omitempty"`
}

// 种子状态简要信息
type TorrentInfo struct {
	ID             int     `json:"id"`
	HashString     string  `json:"hashString"`
	Name           string  `json:"name"`
	Status         int     `json:"status"`
	PercentDone    float64 `json:"percentDone"`
	DownloadDir    string  `json:"downloadDir"`
	TotalSize      int64   `json:"totalSize"`
	LeftUntilDone  int64   `json:"leftUntilDone"`
	UploadedEver   int64   `json:"uploadedEver"`
	DownloadedEver int64   `json:"downloadedEver"`
	Eta            int     `json:"eta"`
	ErrorString    string  `json:"errorString"`
}

// 向 BiglyBT 发送请求并处理 Session ID 鉴权
func (b *BiglyBTClient) sendRPC(method string, args map[string]interface{}) (*RPCResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	reqBody := RPCRequest{
		Method:    method,
		Arguments: args,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(http.MethodPost, b.cfg.RPCURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")
		if b.sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", b.sessionID)
		}
		if b.cfg.Username != "" || b.cfg.Password != "" {
			req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
		}

		resp, err := b.httpClient.Do(req)
		if err != nil {
			// net/http 的错误文本可能带上含 userinfo 或 query 的 URL。
			return nil, fmt.Errorf("连接 BiglyBT 失败: %s", logsafe.Redact(err.Error()))
		}

		// 检查 Transmission 标准 409 握手：提取 X-Transmission-Session-Id 后重试
		if resp.StatusCode == http.StatusConflict {
			newSessionID := resp.Header.Get("X-Transmission-Session-Id")
			resp.Body.Close()
			if newSessionID != "" {
				b.sessionID = newSessionID
				continue
			}
			return nil, errors.New("收到 409 状态码但未获取到 X-Transmission-Session-Id")
		}

		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("BiglyBT RPC 返回异常状态码 (%d): %s", resp.StatusCode, logsafe.Clip(string(data), 180))
		}

		var rpcResp RPCResponse
		if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
			return nil, fmt.Errorf("解析 BiglyBT 响应失败: %w", err)
		}

		if rpcResp.Result != "success" {
			return &rpcResp, fmt.Errorf("BiglyBT 返回执行失败: %s", rpcResp.Result)
		}

		return &rpcResp, nil
	}

	return nil, errors.New("重试后仍未成功连接 BiglyBT")
}

// 向 BiglyBT 投递下载任务，并明确指定物理落盘目录 (download-dir)
func (b *BiglyBTClient) AddTorrent(downloadDir string, rawTorrent []byte, magnetURL string, paused bool) error {
	args := make(map[string]interface{})

	// 关键：指定真实的下载落盘目录 A
	if downloadDir != "" {
		args["download-dir"] = downloadDir
	}
	args["paused"] = paused

	if len(rawTorrent) > 0 {
		args["metainfo"] = base64.StdEncoding.EncodeToString(rawTorrent)
	} else if magnetURL != "" {
		args["filename"] = magnetURL
	} else {
		return errors.New("缺少种子文件内容或磁力链接")
	}

	_, err := b.sendRPC("torrent-add", args)
	return err
}

// 查询 BiglyBT 内部的种子列表
func (b *BiglyBTClient) GetTorrents() ([]TorrentInfo, error) {
	args := map[string]interface{}{
		"fields": []string{
			"id", "hashString", "name", "status", "percentDone",
			"downloadDir", "totalSize", "leftUntilDone",
			"uploadedEver", "downloadedEver", "eta", "errorString",
		},
	}

	resp, err := b.sendRPC("torrent-get", args)
	if err != nil {
		return nil, err
	}

	var getArgs struct {
		Torrents []TorrentInfo `json:"torrents"`
	}
	if err := json.Unmarshal(resp.Arguments, &getArgs); err != nil {
		return nil, fmt.Errorf("反序列化 torrent-get 结果失败: %w", err)
	}

	return getArgs.Torrents, nil
}
