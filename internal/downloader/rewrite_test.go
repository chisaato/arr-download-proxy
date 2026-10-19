package downloader

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRewriteTorrentDownloadDirsKeepsUpstreamFields(t *testing.T) {
	raw := []byte(`{"torrents":[{"id":7,"hashString":"ABC","labels":["全部","做种中"],"files":[{"name":"a.mkv","length":10}],"isFinished":false,"status":6,"leftUntilDone":0,"downloadDir":"/host/cache-mix/anime/downloads/2026-10/Show","seedRatioMode":0},{"id":8,"downloadDir":"/other/place","isFinished":false}]}`)
	out, n, err := RewriteTorrentDownloadDirs(raw, func(dir string) string {
		return strings.Replace(dir, "/host/cache-mix", "/data/cache-mix", 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("转译条数=%d", n)
	}
	var payload struct {
		Torrents []map[string]interface{} `json:"torrents"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Torrents) != 2 {
		t.Fatalf("任务数被改变: %d", len(payload.Torrents))
	}
	first := payload.Torrents[0]
	if first["downloadDir"] != "/data/cache-mix/anime/downloads/2026-10/Show" {
		t.Fatalf("downloadDir=%v", first["downloadDir"])
	}
	if first["isFinished"] != false {
		t.Fatalf("isFinished 被改写: %v", first["isFinished"])
	}
	labels, _ := first["labels"].([]interface{})
	if len(labels) != 2 || labels[0] != "全部" {
		t.Fatalf("labels 丢失: %v", first["labels"])
	}
	files, _ := first["files"].([]interface{})
	if len(files) != 1 {
		t.Fatalf("files 丢失: %v", first["files"])
	}
	if first["seedRatioMode"] != float64(0) || first["status"] != float64(6) || first["leftUntilDone"] != float64(0) {
		t.Fatalf("上游状态字段被改: %#v", first)
	}
	if payload.Torrents[1]["downloadDir"] != "/other/place" {
		t.Fatalf("未映射路径被改: %v", payload.Torrents[1]["downloadDir"])
	}
}

func TestRewriteSessionDownloadDirOnly(t *testing.T) {
	raw := []byte(`{"version":"4.1.0","rpc-version":17,"download-dir":"/host/cache-mix/anime/downloads","speed-limit-down":0}`)
	out, changed, err := RewriteSessionDownloadDir(raw, func(dir string) string {
		return strings.Replace(dir, "/host/cache-mix", "/data/cache-mix", 1)
	})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["download-dir"] != "/data/cache-mix/anime/downloads" {
		t.Fatalf("download-dir=%v", payload["download-dir"])
	}
	if payload["version"] != "4.1.0" || payload["rpc-version"] != float64(17) {
		t.Fatalf("臆造或丢失了会话字段: %#v", payload)
	}
	if _, ok := payload["rpc-version-minimum"]; ok {
		t.Fatal("不该补上上游没有的字段")
	}
}
