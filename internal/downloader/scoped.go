package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const scopedTimeout = 15 * time.Second

// UpstreamError 是请求级 BiglyBT 校验的受控失败。Reason 是固定标记，不含上游正文。
type UpstreamError struct {
	Status   int
	Upstream int
	Retries  int
	Reason   string
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	return e.Reason
}

// ScopedClient 只使用这一次入站请求自己的 Basic，不用配置里的 BiglyBT 密码。
type ScopedClient struct {
	endpoint string
	user     string
	pass     string
	sendAuth bool
	session  string
	retries  int
	upstream int
	http     *http.Client
	mu       sync.Mutex
}

func OpenScoped(ctx context.Context, endpoint, user, pass string, sendAuth bool, timeout time.Duration) (*ScopedClient, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	client := &ScopedClient{
		endpoint: endpoint,
		user:     user,
		pass:     pass,
		sendAuth: sendAuth,
		http:     noRedirectClient(timeout),
	}
	if _, err := client.post(ctx, "session-get", nil); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *ScopedClient) GetTorrents() ([]TorrentInfo, error) {
	resp, err := c.post(context.Background(), "torrent-get", map[string]interface{}{
		"fields": []string{
			"id", "hashString", "name", "status", "percentDone",
			"downloadDir", "totalSize", "leftUntilDone",
			"uploadedEver", "downloadedEver", "eta", "errorString",
		},
	})
	if err != nil {
		return nil, err
	}
	var getArgs struct {
		Torrents []TorrentInfo `json:"torrents"`
	}
	if err := json.Unmarshal(resp.Arguments, &getArgs); err != nil {
		return nil, &UpstreamError{Status: http.StatusBadGateway, Upstream: http.StatusOK, Reason: "bad-json"}
	}
	return getArgs.Torrents, nil
}

func (c *ScopedClient) Trace() (upstream, retries int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.upstream, c.retries
}

// Handshake 把 Sonarr 的空 GET 原样转到 BiglyBT，不重试、不跟随重定向、不读取正文。
func Handshake(ctx context.Context, endpoint, user, pass string, sendAuth bool, inboundSession string, timeout time.Duration) (status, upstream, retries int, sessionID string, err error) {
	if err = validateEndpoint(endpoint); err != nil {
		return http.StatusBadGateway, 0, 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return http.StatusBadGateway, 0, 0, "", &UpstreamError{Status: http.StatusBadGateway, Reason: "upstream"}
	}
	if sendAuth {
		req.SetBasicAuth(user, pass)
	}
	if inboundSession != "" {
		req.Header.Set("X-Transmission-Session-Id", inboundSession)
	}
	resp, err := noRedirectClient(timeout).Do(req)
	if err != nil {
		return mapTransport(err), 0, 0, "", &UpstreamError{Status: mapTransport(err), Reason: reasonForTransport(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return http.StatusUnauthorized, resp.StatusCode, 0, "", nil
	case http.StatusForbidden:
		return http.StatusForbidden, resp.StatusCode, 0, "", nil
	case http.StatusConflict:
		sid := resp.Header.Get("X-Transmission-Session-Id")
		if sid == "" {
			return http.StatusBadGateway, resp.StatusCode, 0, "", &UpstreamError{Status: http.StatusBadGateway, Upstream: resp.StatusCode, Reason: "session-handshake"}
		}
		return http.StatusConflict, resp.StatusCode, 0, sid, nil
	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return http.StatusBadGateway, resp.StatusCode, 0, "", &UpstreamError{Status: http.StatusBadGateway, Upstream: resp.StatusCode, Reason: "redirect"}
		}
		return http.StatusBadGateway, resp.StatusCode, 0, "", &UpstreamError{Status: http.StatusBadGateway, Upstream: resp.StatusCode, Reason: "upstream"}
	}
}

// CallRaw 按调用方原始 arguments 转发，避免重新编码时丢掉 fields/ids。
func (c *ScopedClient) CallRaw(ctx context.Context, method string, arguments json.RawMessage) (*RPCResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload := struct {
		Method    string          `json:"method"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}{Method: method}
	if len(arguments) > 0 && string(arguments) != "null" {
		payload.Arguments = arguments
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &UpstreamError{Status: http.StatusBadGateway, Reason: "upstream"}
	}
	return c.postBytes(ctx, body)
}

func (c *ScopedClient) post(ctx context.Context, method string, args map[string]interface{}) (*RPCResponse, error) {
	body, err := json.Marshal(RPCRequest{Method: method, Arguments: args})
	if err != nil {
		return nil, &UpstreamError{Status: http.StatusBadGateway, Reason: "upstream"}
	}
	return c.postBytes(ctx, body)
}

func (c *ScopedClient) postBytes(ctx context.Context, body []byte) (*RPCResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lastStatus int
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, &UpstreamError{Status: http.StatusBadGateway, Reason: "upstream"}
		}
		req.Header.Set("Content-Type", "application/json")
		if c.sendAuth {
			req.SetBasicAuth(c.user, c.pass)
		}
		if c.session != "" {
			req.Header.Set("X-Transmission-Session-Id", c.session)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			status := mapTransport(err)
			return nil, &UpstreamError{Status: status, Retries: c.retries, Reason: reasonForTransport(err)}
		}
		lastStatus = resp.StatusCode
		c.upstream = resp.StatusCode
		if resp.StatusCode == http.StatusConflict {
			sid := resp.Header.Get("X-Transmission-Session-Id")
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if sid == "" || attempt == 1 {
				return nil, &UpstreamError{Status: http.StatusBadGateway, Upstream: resp.StatusCode, Retries: c.retries, Reason: "session-handshake"}
			}
			c.session = sid
			c.retries++
			continue
		}
		rpcResp, err := finishScoped(resp)
		if err != nil {
			if ue, ok := err.(*UpstreamError); ok {
				ue.Upstream = lastStatus
				ue.Retries = c.retries
			}
			return nil, err
		}
		return rpcResp, nil
	}
	return nil, &UpstreamError{Status: http.StatusBadGateway, Upstream: lastStatus, Retries: c.retries, Reason: "session-handshake"}
}

func finishScoped(resp *http.Response) (*RPCResponse, error) {
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, &UpstreamError{Status: http.StatusUnauthorized, Reason: "unauthorized"}
	case http.StatusForbidden:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, &UpstreamError{Status: http.StatusForbidden, Reason: "forbidden"}
	case http.StatusOK:
		var rpcResp RPCResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rpcResp); err != nil {
			return nil, &UpstreamError{Status: http.StatusBadGateway, Reason: "bad-json"}
		}
		if rpcResp.Result != "success" {
			return nil, &UpstreamError{Status: http.StatusBadGateway, Reason: "result-not-success"}
		}
		return &rpcResp, nil
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, &UpstreamError{Status: http.StatusBadGateway, Reason: "redirect"}
		}
		return nil, &UpstreamError{Status: http.StatusBadGateway, Reason: "upstream"}
	}
}

func validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return &UpstreamError{Status: http.StatusBadGateway, Reason: "rejected-url"}
	}
	return nil
}

func noRedirectClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = scopedTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func mapTransport(err error) int {
	if isTimeout(err) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func reasonForTransport(err error) string {
	if isTimeout(err) {
		return "timeout"
	}
	return "upstream"
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
