package logsafe

import "regexp"

var (
	// 去掉 URL 里的 userinfo，避免 Basic 凭据出现在 net/http 的错误文本中。
	urlUserinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@]+@`)
	magnetLink  = regexp.MustCompile(`(?i)magnet:\?\S+`)
	urlQuery    = regexp.MustCompile(`(?i)(https?://[^\s?]+)\?\S+`)
	// 键名后的引号和 JSON 数组都要能盖住，例如 "api_key":["secret"]。
	secretField = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|password|passwd|pwd|token)"?(\s*[:=]\s*)(?:\[[^\]]*\]|"[^"]*"|'[^']*'|\S+)`)
	basicAuth   = regexp.MustCompile(`(?i)\b(basic\s+)[A-Za-z0-9+/=]+`)
	// 种子 metainfo 一类的长 base64。40 位 info hash 短于 64，不会被吃掉。
	longBase64 = regexp.MustCompile(`[A-Za-z0-9+/]{64,}={0,2}`)
)

var debugEnabled bool

func SetDebug(enabled bool) {
	debugEnabled = enabled
}

func Debug() bool {
	return debugEnabled
}

// Redact 去掉日志里不该出现的凭据、完整磁力、URL query 和种子 base64。
func Redact(s string) string {
	s = urlUserinfo.ReplaceAllString(s, "${1}")
	s = magnetLink.ReplaceAllString(s, "magnet:?[redacted]")
	s = urlQuery.ReplaceAllString(s, "${1}?[redacted]")
	s = basicAuth.ReplaceAllString(s, "${1}[redacted]")
	s = secretField.ReplaceAllString(s, "${1}${2}[redacted]")
	s = longBase64.ReplaceAllString(s, "[redacted-base64]")
	return s
}

func Error(err error) string {
	if err == nil {
		return ""
	}
	return Redact(err.Error())
}

// Clip 只做脱敏。maxRunes 仍保留给已有调用点，但不再按任何长度截断。
func Clip(s string, maxRunes int) string {
	return Redact(s)
}
