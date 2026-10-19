package pathmapper

import (
	"testing"

	"arr-download-proxy/internal/config"
)

func TestResolveDownloadDir(t *testing.T) {
	const fallback = "/data/cache-mix/anime/downloads-unmatched-default"

	mapper := NewPathMapper(config.PathRulesConfig{
		DefaultDownloadDir: fallback,
		DiskMappings: []config.DiskMapping{
			{MediaPrefix: "/data/anime", DownloadDir: "/data/anime/downloads"},
			{MediaPrefix: "/data/cache-mix/anime/media", DownloadDir: "/data/cache-mix/anime/downloads"},
			{MediaPrefix: "/data/cache-mix/anime/categorized", DownloadDir: "/data/cache-mix/anime/downloads"},
			{MediaPrefix: "/data/anime/special/", DownloadDir: "/data/anime/special-downloads/"},
		},
	})

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "media 保留剧名后缀",
			input: "/data/cache-mix/anime/media/某剧",
			want:  "/data/cache-mix/anime/downloads/某剧",
		},
		{
			name:  "media 保留全部多级后缀",
			input: "/data/cache-mix/anime/media/某剧/Season 01",
			want:  "/data/cache-mix/anime/downloads/某剧/Season 01",
		},
		{
			name:  "categorized 保留月度分类后缀",
			input: "/data/cache-mix/anime/categorized/2026-10/某剧",
			want:  "/data/cache-mix/anime/downloads/2026-10/某剧",
		},
		{
			name:  "精确命中前缀本身",
			input: "/data/cache-mix/anime/media",
			want:  "/data/cache-mix/anime/downloads",
		},
		{
			name:  "目标路径与配置前缀的尾斜杠都去掉",
			input: "/data/cache-mix/anime/categorized/2026-10/某剧/",
			want:  "/data/cache-mix/anime/downloads/2026-10/某剧",
		},
		{
			name:  "更长前缀优先并保留相对后缀",
			input: "/data/anime/special/2026-10/某剧",
			want:  "/data/anime/special-downloads/2026-10/某剧",
		},
		{
			name:  "较短前缀仍保留自己的后缀",
			input: "/data/anime/library/某剧",
			want:  "/data/anime/downloads/library/某剧",
		},
		{
			name:  "路径段边界不匹配 media-other",
			input: "/data/cache-mix/anime/media-other/某剧",
			want:  fallback,
		},
		{
			name:  "未命中走兜底",
			input: "/data/elsewhere/某剧",
			want:  fallback,
		},
		{
			name:  "空路径走兜底",
			input: "",
			want:  fallback,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapper.ResolveDownloadDir(tt.input)
			if got != tt.want {
				t.Fatalf("输入 %s\n实际 %s\n期望 %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveDownloadDirRootPrefix(t *testing.T) {
	mapper := NewPathMapper(config.PathRulesConfig{
		DefaultDownloadDir: "/unused-default",
		DiskMappings: []config.DiskMapping{
			{MediaPrefix: "/", DownloadDir: "/downloads"},
			{MediaPrefix: "/data/cache-mix/anime/media", DownloadDir: "/data/cache-mix/anime/downloads"},
		},
	})

	got := mapper.ResolveDownloadDir("/other/某剧")
	want := "/downloads/other/某剧"
	if got != want {
		t.Fatalf("根前缀: 实际 %s，期望 %s", got, want)
	}

	got = mapper.ResolveDownloadDir("/data/cache-mix/anime/media/某剧")
	want = "/data/cache-mix/anime/downloads/某剧"
	if got != want {
		t.Fatalf("更长前缀应盖过根前缀: 实际 %s，期望 %s", got, want)
	}

	// 只配置根 "/" 时，修剪后的空前缀匹配整条路径，相对后缀就是原路径。
	rootOnly := NewPathMapper(config.PathRulesConfig{
		DefaultDownloadDir: "/unused-default",
		DiskMappings: []config.DiskMapping{
			{MediaPrefix: "/", DownloadDir: "/dl/"},
		},
	})
	got = rootOnly.ResolveDownloadDir("/")
	want = "/dl"
	if got != want {
		t.Fatalf("根路径自身: 实际 %s，期望 %s", got, want)
	}
}

func TestTranslateToDockerPathKeepsExistingRules(t *testing.T) {
	mapper := NewPathMapper(config.PathRulesConfig{
		DockerMappings: []config.DockerMapping{
			{HostPath: "/host/cache-mix", ContainerPath: "/data/cache-mix/"},
			{HostPath: "/host/cache-mix/anime/downloads", ContainerPath: "/should-not-win"},
		},
	})

	got := mapper.TranslateToDockerPath("/host/cache-mix/anime/downloads/2026-10/某剧/")
	want := "/data/cache-mix/anime/downloads/2026-10/某剧"
	if got != want {
		t.Fatalf("后缀转译: 实际 %s，期望 %s", got, want)
	}

	// 既有实现是第一次 HasPrefix 命中即返回，不做路径段边界判断。
	loose := NewPathMapper(config.PathRulesConfig{
		DockerMappings: []config.DockerMapping{
			{HostPath: "/host/downloads", ContainerPath: "/downloads"},
		},
	})
	got = loose.TranslateToDockerPath("/host/downloads-other/某剧")
	want = "/downloads-other/某剧"
	if got != want {
		t.Fatalf("非边界前缀: 实际 %s，期望 %s", got, want)
	}

	if got := loose.TranslateToDockerPath("/unmapped/某剧"); got != "/unmapped/某剧" {
		t.Fatalf("未配置映射应原样返回，实际 %s", got)
	}
}
