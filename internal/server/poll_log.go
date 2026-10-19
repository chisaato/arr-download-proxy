package server

// torrentGetShouldLog 控制 torrent-get 成功日志。
// Sonarr 会持续轮询；数量和路径转译都没变时保持安静，debug 时每次都记。
func torrentGetShouldLog(debug, seen bool, prevCount, prevTranslated, count, translated int) bool {
	if debug || !seen {
		return true
	}
	return prevCount != count || prevTranslated != translated
}
