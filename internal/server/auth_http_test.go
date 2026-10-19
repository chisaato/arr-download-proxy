package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"arr-download-proxy/internal/config"
	"arr-download-proxy/internal/downloader"
	"arr-download-proxy/internal/pathmapper"
	"arr-download-proxy/internal/sonarr"
	"arr-download-proxy/internal/store"
)

type bbtHit struct {
	httpMethod string
	user       string
	pass       string
	hasAuth    bool
	session    string
	rpcMethod  string
}

type fakeBBT struct {
	mu   sync.Mutex
	hits []bbtHit
	mode string
}

func (f *fakeBBT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, hasAuth := r.BasicAuth()
	hit := bbtHit{
		httpMethod: r.Method,
		user:       user,
		pass:       pass,
		hasAuth:    hasAuth,
		session:    r.Header.Get("X-Transmission-Session-Id"),
	}
	if r.Method == http.MethodPost {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hit.rpcMethod = body.Method
	}
	f.mu.Lock()
	f.hits = append(f.hits, hit)
	mode := f.mode
	f.mu.Unlock()

	switch mode {
	case "hang":
		time.Sleep(time.Second)
		return
	case "redirect":
		http.Redirect(w, r, "http://127.0.0.1:1/hidden", http.StatusFound)
		return
	case "no-session":
		w.WriteHeader(http.StatusConflict)
		return
	case "always-409":
		w.Header().Set("X-Transmission-Session-Id", "again")
		w.WriteHeader(http.StatusConflict)
		return
	case "status-500":
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "upstream-secret-body")
		return
	case "bad-result":
		_, _ = io.WriteString(w, `{"result":"nope-secret"}`)
		return
	case "forbid":
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if r.Method == http.MethodGet {
		w.Header().Set("X-Transmission-Session-Id", "hs-"+user)
		w.WriteHeader(http.StatusConflict)
		return
	}
	if mode == "anonymous" && !hasAuth {
		_, _ = io.WriteString(w, `{"result":"success","arguments":{"torrents":[]}}`)
		return
	}
	if !hasAuth || pass != "pw-"+user {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if hit.session == "" {
		w.Header().Set("X-Transmission-Session-Id", "sid-"+user)
		w.WriteHeader(http.StatusConflict)
		return
	}
	if hit.session != "sid-"+user {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = io.WriteString(w, `{"result":"success","arguments":{"torrents":[]}}`)
}

func (f *fakeBBT) snapshot() []bbtHit {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bbtHit, len(f.hits))
	copy(out, f.hits)
	return out
}

func testHandler(t *testing.T, rpcURL string) *Server {
	t.Helper()
	db, err := store.NewStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{
		BiglyBT: config.BiglyBTConfig{
			RPCURL:   rpcURL,
			Username: "config-user",
			Password: "config-secret",
		},
		PathRules: config.PathRulesConfig{DefaultDownloadDir: "/downloads"},
	}
	srv := NewServer(cfg, db, downloader.NewBiglyBTClient(cfg.BiglyBT), sonarr.NewClient(config.SonarrConfig{}), pathmapper.NewPathMapper(cfg.PathRules))
	return srv
}

func postRPC(handler http.Handler, user, pass, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/transmission/rpc", strings.NewReader(`{"method":"`+method+`"}`))
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestPostAuthUsesClientBasicNotConfig(t *testing.T) {
	fake := &fakeBBT{}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	handler := testHandler(t, upstream.URL).Handler()

	ok := postRPC(handler, "ann", "pw-ann", "torrent-get")
	if ok.Code != http.StatusOK {
		t.Fatalf("正确凭据 status=%d body=%s", ok.Code, ok.Body.String())
	}
	var payload struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &payload); err != nil || payload.Result != "success" {
		t.Fatalf("业务响应异常: %s", ok.Body.String())
	}

	denied := postRPC(handler, "ann", "wrong", "torrent-get")
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("错误凭据 status=%d", denied.Code)
	}
	if denied.Header().Get("WWW-Authenticate") != transmissionChallenge {
		t.Fatalf("挑战头=%q", denied.Header().Get("WWW-Authenticate"))
	}
	if strings.Contains(denied.Body.String(), "upstream-secret") {
		t.Fatalf("上游正文被转发: %s", denied.Body.String())
	}

	hits := fake.snapshot()
	sawTorrent := false
	for _, hit := range hits {
		if hit.pass == "config-secret" || hit.user == "config-user" {
			t.Fatalf("偷用了配置凭据: %+v", hit)
		}
		if hit.rpcMethod == "torrent-get" {
			sawTorrent = true
			if hit.user != "ann" || hit.pass != "pw-ann" {
				t.Fatalf("torrent-get 凭据混淆: %+v", hit)
			}
		}
	}
	if !sawTorrent {
		t.Fatal("通过校验后没有用请求级客户端查询 torrent-get")
	}
	for _, hit := range hits[len(hits)-1:] {
		_ = hit
	}
	lastDenied := false
	for _, hit := range hits {
		if hit.pass == "wrong" && hit.rpcMethod == "torrent-get" {
			lastDenied = true
		}
	}
	if lastDenied {
		t.Fatal("401 之后仍触发了业务 torrent-get")
	}
}

func TestMissingBasicDoesNotUseConfiguredPassword(t *testing.T) {
	fake := &fakeBBT{mode: "anonymous"}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	rr := postRPC(testHandler(t, upstream.URL).Handler(), "", "", "session-get")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, hit := range fake.snapshot() {
		if hit.hasAuth {
			t.Fatalf("无入站 Basic 时仍发送了认证头: %+v", hit)
		}
	}
}

func TestVerifyFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		status int
	}{
		{name: "连续409", mode: "always-409", status: http.StatusBadGateway},
		{name: "409缺少session", mode: "no-session", status: http.StatusBadGateway},
		{name: "上游500", mode: "status-500", status: http.StatusBadGateway},
		{name: "result不是success", mode: "bad-result", status: http.StatusBadGateway},
		{name: "重定向", mode: "redirect", status: http.StatusBadGateway},
		{name: "403保留", mode: "forbid", status: http.StatusForbidden},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeBBT{mode: tt.mode}
			upstream := httptest.NewServer(fake)
			defer upstream.Close()
			rr := postRPC(testHandler(t, upstream.URL).Handler(), "ann", "pw-ann", "torrent-get")
			if rr.Code != tt.status {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "secret") || strings.Contains(rr.Body.String(), "hidden") {
				t.Fatalf("响应泄漏上游内容: %s", rr.Body.String())
			}
			if rr.Header().Get("Location") != "" {
				t.Fatal("转发了重定向")
			}
			for _, hit := range fake.snapshot() {
				if hit.rpcMethod == "torrent-get" {
					t.Fatal("校验失败后仍执行了业务")
				}
			}
		})
	}
}

func TestNetworkAndTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	rr := postRPC(testHandler(t, "http://"+addr+"/transmission/rpc").Handler(), "ann", "pw-ann", "session-get")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("网络错误 status=%d", rr.Code)
	}

	fake := &fakeBBT{mode: "hang"}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	srv := testHandler(t, upstream.URL)
	srv.authTimeout = 100 * time.Millisecond
	rr = postRPC(srv.Handler(), "ann", "pw-ann", "session-get")
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("超时 status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRejectRPCURLUserinfo(t *testing.T) {
	fake := &fakeBBT{}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword("config-user", "config-secret")
	rr := postRPC(testHandler(t, parsed.String()).Handler(), "ann", "pw-ann", "session-get")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", rr.Code)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("带 userinfo 的 RPC URL 仍发出了请求")
	}
}

func TestGETJSONAndPlainHandshake(t *testing.T) {
	fake := &fakeBBT{}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	handler := testHandler(t, upstream.URL).Handler()

	empty := httptest.NewRequest(http.MethodGet, "/transmission/rpc?json=", nil)
	empty.SetBasicAuth("ann", "pw-ann")
	emptyRR := httptest.NewRecorder()
	handler.ServeHTTP(emptyRR, empty)
	if emptyRR.Code != http.StatusBadRequest {
		t.Fatalf("空 json status=%d", emptyRR.Code)
	}

	q := url.Values{}
	q.Set("json", `{"method":"session-get"}`)
	getReq := httptest.NewRequest(http.MethodGet, "/transmission/rpc?"+q.Encode(), nil)
	getReq.SetBasicAuth("ann", "pw-ann")
	getRR := httptest.NewRecorder()
	handler.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK || !strings.Contains(getRR.Body.String(), `"result":"success"`) {
		t.Fatalf("json GET status=%d body=%s", getRR.Code, getRR.Body.String())
	}

	plain := httptest.NewRequest(http.MethodGet, "/transmission/rpc", nil)
	plain.SetBasicAuth("ann", "pw-ann")
	plain.Header.Set("X-Transmission-Session-Id", "from-client")
	plainRR := httptest.NewRecorder()
	handler.ServeHTTP(plainRR, plain)
	if plainRR.Code != http.StatusConflict {
		t.Fatalf("空 GET status=%d body=%s", plainRR.Code, plainRR.Body.String())
	}
	if plainRR.Header().Get("X-Transmission-Session-Id") != "hs-ann" {
		t.Fatalf("session=%q", plainRR.Header().Get("X-Transmission-Session-Id"))
	}
	if plainRR.Body.Len() != 0 {
		t.Fatalf("握手暴露了正文: %s", plainRR.Body.String())
	}
	sawGET := false
	for _, hit := range fake.snapshot() {
		if hit.httpMethod == http.MethodGet {
			sawGET = true
			if hit.session != "from-client" || hit.user != "ann" || hit.pass != "pw-ann" {
				t.Fatalf("握手没有原样转发给 BBT: %+v", hit)
			}
		}
	}
	if !sawGET {
		t.Fatal("空 GET 没有转发给 BBT")
	}
}

func TestSonarrGetChallengeThenPost(t *testing.T) {
	fake := &fakeBBT{}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	handler := testHandler(t, upstream.URL).Handler()

	getReq := httptest.NewRequest(http.MethodGet, "/transmission/rpc", nil)
	getReq.SetBasicAuth("sonarr", "pw-sonarr")
	getRR := httptest.NewRecorder()
	handler.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusConflict {
		t.Fatalf("GET status=%d", getRR.Code)
	}
	sid := getRR.Header().Get("X-Transmission-Session-Id")
	if sid == "" {
		t.Fatal("GET 没有返回 session")
	}

	postReq := httptest.NewRequest(http.MethodPost, "/transmission/rpc", strings.NewReader(`{"method":"session-get"}`))
	postReq.SetBasicAuth("sonarr", "pw-sonarr")
	postReq.Header.Set("X-Transmission-Session-Id", sid)
	postRR := httptest.NewRecorder()
	handler.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusOK || !strings.Contains(postRR.Body.String(), `"result":"success"`) {
		t.Fatalf("POST status=%d body=%s", postRR.Code, postRR.Body.String())
	}
}

func TestConcurrentCredentialsDoNotMix(t *testing.T) {
	fake := &fakeBBT{}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	handler := testHandler(t, upstream.URL).Handler()

	var wg sync.WaitGroup
	errCh := make(chan string, 20)
	for i := 0; i < 10; i++ {
		for _, user := range []string{"a", "b"} {
			wg.Add(1)
			go func(user string) {
				defer wg.Done()
				rr := postRPC(handler, user, "pw-"+user, "session-get")
				if rr.Code != http.StatusOK {
					errCh <- user + ":" + rr.Body.String()
				}
			}(user)
		}
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
	for _, hit := range fake.snapshot() {
		if hit.hasAuth && hit.pass != "pw-"+hit.user {
			t.Fatalf("并发凭据串用: %+v", hit)
		}
	}
}
