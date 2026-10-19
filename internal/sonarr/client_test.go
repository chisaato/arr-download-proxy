package sonarr

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arr-download-proxy/internal/config"
)

func TestHistoryQueryUsesNumericGrabbedAndKeepsLongValidationError(t *testing.T) {
	longDetail := strings.Repeat("validation-detail-", 40)
	var sawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") == "" {
			t.Fatal("请求没有带 API key 头")
		}
		sawQuery = r.URL.RawQuery
		if r.URL.Query().Get("eventType") == "grabbed" {
			t.Fatal("仍在使用字符串 eventType")
		}
		body := `{"type":"https://tools.ietf.org/html/rfc7231#section-6.5.1","title":"One or more validation errors occurred.","status":400,"errors":{"eventType":["The value is not valid. ` + longDetail + `"],"api_key":["supersecret"]}} magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Show Authorization: Basic QWxhZGRpbjpvcGVu`
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	client := NewClient(config.SonarrConfig{BaseURL: srv.URL, APIKey: "local-test-key", TimeoutSeconds: 2})
	_, _, err := client.QuerySeriesPathWithRetry("ABCDEF", 2, 0)
	if err == nil {
		t.Fatal("400 应返回错误")
	}
	got := err.Error()
	if strings.Contains(got, "未找到 Hash") {
		t.Fatalf("API 失败被说成没找到记录: %s", got)
	}
	if !strings.Contains(got, "Sonarr API 失败") {
		t.Fatalf("没有标明 API 失败: %s", got)
	}
	if !strings.Contains(got, longDetail) {
		t.Fatal("长 validation 文本被截断")
	}
	if strings.Contains(got, "truncated") {
		t.Fatalf("仍有截断标记: %s", got)
	}
	if strings.Contains(got, "supersecret") || strings.Contains(got, "QWxhZGRpbjpvcGVu") || strings.Contains(got, "magnet:?xt=") {
		t.Fatalf("凭据或磁力泄漏: %s", got)
	}
	if !strings.Contains(got, "The value is not valid.") || !strings.Contains(got, `"errors"`) {
		t.Fatalf("validation 字段不完整: %s", got)
	}
	if !strings.Contains(sawQuery, "eventType=1") || !strings.Contains(sawQuery, "includeSeries=true") {
		t.Fatalf("查询参数不对: %s", sawQuery)
	}
}

func TestHistoryDistinguishesMissingRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"records":[{"downloadId":"other","eventType":"grabbed","series":{"title":"别的剧","path":"/data/cache-mix/anime/categorized/2026-10/别的剧"}}]}`)
	}))
	defer srv.Close()

	client := NewClient(config.SonarrConfig{BaseURL: srv.URL, APIKey: "local-test-key", TimeoutSeconds: 2})
	_, _, err := client.QuerySeriesPathWithRetry("missinghash", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "未找到 Hash") {
		t.Fatalf("无记录应明确区分: %v", err)
	}
	if strings.Contains(err.Error(), "Sonarr API 失败") {
		t.Fatalf("没记录被说成 API 失败: %v", err)
	}
}

func TestHistoryReturnsSeriesPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"records":[{"downloadId":"AbC","eventType":"grabbed","series":{"title":"某剧","path":"/data/cache-mix/anime/categorized/2026-10/某剧"}}]}`)
	}))
	defer srv.Close()

	client := NewClient(config.SonarrConfig{BaseURL: srv.URL, APIKey: "local-test-key", TimeoutSeconds: 2})
	title, path, err := client.QuerySeriesPathWithRetry("abc", 1, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if title != "某剧" || path != "/data/cache-mix/anime/categorized/2026-10/某剧" {
		t.Fatalf("路径不对: %s %s", title, path)
	}
}

func TestHistoryNetworkErrorIsNotReportedAsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	client := NewClient(config.SonarrConfig{BaseURL: srv.URL, APIKey: "local-test-key", TimeoutSeconds: 1})
	_, _, err := client.QuerySeriesPathWithRetry("abc", 1, 0)
	if err == nil || strings.Contains(err.Error(), "未找到 Hash") || !strings.Contains(err.Error(), "Sonarr API 失败") {
		t.Fatalf("网络错误被掩盖: %v", err)
	}
}
