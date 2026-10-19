package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"arr-download-proxy/internal/config"
	"arr-download-proxy/internal/downloader"
	"arr-download-proxy/internal/pathmapper"
	"arr-download-proxy/internal/sonarr"
	"arr-download-proxy/internal/store"
)

type fidelityFake struct {
	mu         sync.Mutex
	gets       []map[string]interface{}
	sessionGot int
}

func (f *fidelityFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("X-Transmission-Session-Id", "hs")
		w.WriteHeader(http.StatusConflict)
		return
	}
	user, pass, _ := r.BasicAuth()
	var body struct {
		Method    string                 `json:"method"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.Header.Get("X-Transmission-Session-Id") == "" {
		w.Header().Set("X-Transmission-Session-Id", "sid-"+user)
		w.WriteHeader(http.StatusConflict)
		return
	}
	if pass != "pw-"+user {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch body.Method {
	case "session-get":
		f.mu.Lock()
		f.sessionGot++
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"result":"success","arguments":{"version":"4.1.0","rpc-version":17,"download-dir":"/host/cache-mix/anime/downloads","speed-limit-down":0}}`)
	case "torrent-get":
		f.mu.Lock()
		f.gets = append(f.gets, body.Arguments)
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"result":"success","tag":99,"arguments":{"torrents":[{"hashString":"ABC","labels":["全部","做种中"],"files":[{"name":"a.mkv","length":10}],"isFinished":false,"status":6,"leftUntilDone":0,"downloadDir":"/host/cache-mix/anime/downloads/2026-10/Show","seedRatioMode":0}]}}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestTorrentAndSessionFidelity(t *testing.T) {
	fake := &fidelityFake{}
	upstream := httptest.NewServer(fake)
	defer upstream.Close()
	db, err := store.NewStore(t.TempDir() + "/fidelity.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{
		BiglyBT: config.BiglyBTConfig{RPCURL: upstream.URL, Username: "config-user", Password: "config-secret"},
		PathRules: config.PathRulesConfig{
			DockerMappings: []config.DockerMapping{{
				HostPath:      "/host/cache-mix",
				ContainerPath: "/data/cache-mix",
			}},
		},
	}
	handler := NewServer(cfg, db, downloader.NewBiglyBTClient(cfg.BiglyBT), sonarr.NewClient(config.SonarrConfig{}), pathmapper.NewPathMapper(cfg.PathRules)).Handler()

	getBody := `{"method":"torrent-get","tag":7,"arguments":{"fields":["id","hashString","labels","files","isFinished","status","leftUntilDone","downloadDir","seedRatioMode"],"ids":[3]}}`
	req := httptest.NewRequest(http.MethodPost, "/transmission/rpc", strings.NewReader(getBody))
	req.SetBasicAuth("ann", "pw-ann")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("torrent-get status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Tag       int `json:"tag"`
		Arguments struct {
			Torrents []map[string]interface{} `json:"torrents"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Tag != 7 {
		t.Fatalf("tag=%d", got.Tag)
	}
	if len(got.Arguments.Torrents) != 1 {
		t.Fatalf("任务数=%d", len(got.Arguments.Torrents))
	}
	item := got.Arguments.Torrents[0]
	if item["downloadDir"] != "/data/cache-mix/anime/downloads/2026-10/Show" {
		t.Fatalf("downloadDir=%v", item["downloadDir"])
	}
	if item["isFinished"] != false || item["status"] != float64(6) || item["leftUntilDone"] != float64(0) {
		t.Fatalf("上游完成语义被改: %#v", item)
	}
	if _, ok := item["labels"]; !ok || item["files"] == nil || item["seedRatioMode"] == nil {
		t.Fatalf("字段被窄结构抹掉: %#v", item)
	}
	fake.mu.Lock()
	gets := append([]map[string]interface{}{}, fake.gets...)
	fake.mu.Unlock()
	if len(gets) != 1 {
		t.Fatalf("torrent-get 次数=%d", len(gets))
	}
	fields, _ := gets[0]["fields"].([]interface{})
	joined := make([]string, 0, len(fields))
	for _, field := range fields {
		joined = append(joined, field.(string))
	}
	if strings.Join(joined, ",") != "id,hashString,labels,files,isFinished,status,leftUntilDone,downloadDir,seedRatioMode" {
		t.Fatalf("fields 未原样转发: %v", fields)
	}
	if gets[0]["ids"] == nil {
		t.Fatal("ids 未转发")
	}

	sreq := httptest.NewRequest(http.MethodPost, "/transmission/rpc", strings.NewReader(`{"method":"session-get","tag":8}`))
	sreq.SetBasicAuth("ann", "pw-ann")
	srr := httptest.NewRecorder()
	handler.ServeHTTP(srr, sreq)
	var session struct {
		Tag       int                    `json:"tag"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(srr.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Tag != 8 {
		t.Fatalf("session tag=%d", session.Tag)
	}
	if session.Arguments["download-dir"] != "/data/cache-mix/anime/downloads" {
		t.Fatalf("download-dir=%v", session.Arguments["download-dir"])
	}
	if strings.Contains(srr.Body.String(), "/host/cache-mix") {
		t.Fatalf("宿主路径泄漏: %s", srr.Body.String())
	}
	if session.Arguments["version"] != "4.1.0" || session.Arguments["rpc-version"] != float64(17) {
		t.Fatalf("会话字段未保真: %#v", session.Arguments)
	}
	if _, ok := session.Arguments["rpc-version-minimum"]; ok {
		t.Fatal("补上了上游没有的字段")
	}
}
