package pathmapper

import (
	"strings"

	"arr-download-proxy/internal/config"
)

type PathMapper struct {
	cfg config.PathRulesConfig
}

func NewPathMapper(cfg config.PathRulesConfig) *PathMapper {
	return &PathMapper{cfg: cfg}
}

// 根据目标剧集最终存储路径，确定同一文件系统上的下载目录，供后续硬链接。
// 命中 media_prefix 时，用 download_dir 替换该前缀，并保留后面的全部相对后缀。
// 因此 media、categorized 会落到 downloads 下的对应子路径，而不是固定的下载根目录。
// 前缀按路径段比较：路径等于前缀，或接下来是 "/"，才算命中；/media 不会匹配 /media-other。
// 多条规则取最长前缀。目标路径为空或没有任何规则命中时，返回默认下载目录。
func (m *PathMapper) ResolveDownloadDir(destSeriesPath string) string {
	if destSeriesPath == "" {
		return m.cfg.DefaultDownloadDir
	}

	cleanDest := strings.TrimRight(destSeriesPath, "/")

	matched := ""
	longestPrefixLen := -1
	found := false

	for _, mapping := range m.cfg.DiskMappings {
		prefix := strings.TrimRight(mapping.MediaPrefix, "/")
		if !hasPathSegmentPrefix(cleanDest, prefix) {
			continue
		}
		if len(prefix) > longestPrefixLen {
			longestPrefixLen = len(prefix)
			matched = replacePrefix(cleanDest, prefix, mapping.DownloadDir)
			found = true
		}
	}

	if !found {
		return m.cfg.DefaultDownloadDir
	}
	return matched
}

// 将真实的物理下载目录转换为 Sonarr Docker 容器内部识别的映射路径。
// 当 Sonarr 向代理轮询任务状态时，返回容器视角路径。
// 这里仍按原有的字符串前缀替换，不做路径段边界判断，也不改成最长匹配。
func (m *PathMapper) TranslateToDockerPath(hostPath string) string {
	cleanHost := strings.TrimRight(hostPath, "/")

	for _, mapping := range m.cfg.DockerMappings {
		prefix := strings.TrimRight(mapping.HostPath, "/")
		if strings.HasPrefix(cleanHost, prefix) {
			return replacePrefix(cleanHost, prefix, mapping.ContainerPath)
		}
	}

	// 如果没有特别配置映射，原样返回
	return hostPath
}

// hasPathSegmentPrefix 判断 path 是否就是 prefix，或位于 prefix 的下一级路径中。
// 根路径 "/" 经 TrimRight 后变成空字符串。空前缀与 TranslateToDockerPath 的既有约定一致，匹配任意路径。
func hasPathSegmentPrefix(path, prefix string) bool {
	if prefix == "" {
		return true
	}
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}

// replacePrefix 用 replacement 替换 path 中的 prefix，并保留 prefix 之后的全部相对后缀。
// path 与 prefix 都应已经去掉末尾斜杠。根前缀为空时，相对后缀就是整个 path。
func replacePrefix(path, prefix, replacement string) string {
	relative := strings.TrimPrefix(path, prefix)
	return strings.TrimRight(replacement, "/") + relative
}
