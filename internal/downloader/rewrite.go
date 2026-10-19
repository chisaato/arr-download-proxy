package downloader

import "encoding/json"

// RewriteTorrentDownloadDirs 只改 torrent 对象里的 downloadDir，其余字段原样保留。
// 返回转译条数。没有 downloadDir 的任务不改写。
func RewriteTorrentDownloadDirs(arguments json.RawMessage, rewrite func(string) string) (json.RawMessage, int, error) {
	if len(arguments) == 0 || string(arguments) == "null" {
		return arguments, 0, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &payload); err != nil {
		return nil, 0, err
	}
	rawTorrents, ok := payload["torrents"]
	if !ok || len(rawTorrents) == 0 || string(rawTorrents) == "null" {
		return arguments, 0, nil
	}
	var torrents []json.RawMessage
	if err := json.Unmarshal(rawTorrents, &torrents); err != nil {
		return nil, 0, err
	}
	changed := 0
	for i, raw := range torrents {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, 0, err
		}
		dirRaw, ok := obj["downloadDir"]
		if !ok {
			continue
		}
		var dir string
		if err := json.Unmarshal(dirRaw, &dir); err != nil {
			continue
		}
		next := rewrite(dir)
		if next == dir {
			continue
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return nil, 0, err
		}
		obj["downloadDir"] = encoded
		rewritten, err := json.Marshal(obj)
		if err != nil {
			return nil, 0, err
		}
		torrents[i] = rewritten
		changed++
	}
	if changed == 0 {
		return arguments, 0, nil
	}
	encoded, err := json.Marshal(torrents)
	if err != nil {
		return nil, 0, err
	}
	payload["torrents"] = encoded
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	return out, changed, nil
}

// RewriteSessionDownloadDir 只改 session-get 的 download-dir，避免把宿主路径交给容器里的 Sonarr。
func RewriteSessionDownloadDir(arguments json.RawMessage, rewrite func(string) string) (json.RawMessage, bool, error) {
	if len(arguments) == 0 || string(arguments) == "null" {
		return arguments, false, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &payload); err != nil {
		return nil, false, err
	}
	raw, ok := payload["download-dir"]
	if !ok {
		return arguments, false, nil
	}
	var dir string
	if err := json.Unmarshal(raw, &dir); err != nil {
		return arguments, false, nil
	}
	next := rewrite(dir)
	if next == dir {
		return arguments, false, nil
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return nil, false, err
	}
	payload["download-dir"] = encoded
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
