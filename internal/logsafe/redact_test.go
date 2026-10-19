package logsafe

import (
	"strings"
	"testing"
)

func TestRedactKeepsHashAndDropsSecrets(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	input := "dial " + `http://alice:s3cret@127.0.0.1:9092/transmission/rpc?apikey=abc123` +
		" Authorization: Basic QWxhZGRpbjpvcGVu + magnet:?xt=urn:btih:" + hash + "&dn=Show" +
		" api_key=supersecret token: abc hash=" + hash +
		" metainfo=" + "QUJDREVG" + "QUJDREVGQUJDREVGQUJDREVGQUJDREVGQUJDREVGQUJDREVGQUJDREVGQUJDREVGQUJD"

	got := Redact(input)

	if want := "http://127.0.0.1:9092/transmission/rpc?[redacted]"; !contains(got, want) {
		t.Fatalf("URL userinfo/query 未脱敏: %s", got)
	}
	if contains(got, "s3cret") || contains(got, "alice:") || contains(got, "apikey=abc123") || contains(got, "supersecret") {
		t.Fatalf("凭据泄漏: %s", got)
	}
	if contains(got, "magnet:?xt=") {
		t.Fatalf("完整磁力仍在: %s", got)
	}
	if !contains(got, "magnet:?[redacted]") {
		t.Fatalf("磁力未替换: %s", got)
	}
	if contains(got, "QWxhZGRpbjpvcGVu") {
		t.Fatalf("Basic 凭据仍在: %s", got)
	}
	if !contains(got, "[redacted-base64]") {
		t.Fatalf("长 base64 未脱敏: %s", got)
	}
	if !contains(got, "hash="+hash) {
		t.Fatalf("info hash 被误伤: %s", got)
	}
	jsonSecret := Redact(`{"errors":{"api_key":["supersecret"],"eventType":["The value is not valid."]}}`)
	if contains(jsonSecret, "supersecret") || !contains(jsonSecret, "The value is not valid.") {
		t.Fatalf("JSON 校验错误脱敏不正确: %s", jsonSecret)
	}
}

func TestClipDoesNotTruncate(t *testing.T) {
	detail := repeat("validation-detail-", 40)
	got := Clip("password=hunter2 "+detail, 12)
	if contains(got, "hunter2") {
		t.Fatalf("凭据未脱敏: %s", got)
	}
	if contains(got, "truncated") {
		t.Fatalf("仍在截断: %s", got)
	}
	if !contains(got, detail) {
		t.Fatal("完整错误文本被丢掉了")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
