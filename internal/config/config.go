package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// 服务总配置
type Config struct {
	// 服务器监听配置
	Server ServerConfig `yaml:"server"`
	// 数据库配置
	Database DatabaseConfig `yaml:"database"`
	// BiglyBT 连接配置
	BiglyBT BiglyBTConfig `yaml:"biglybt"`
	// Sonarr 连接配置
	Sonarr SonarrConfig `yaml:"sonarr"`
	// 多盘与路径映射规则
	PathRules PathRulesConfig `yaml:"path_rules"`
}

type ServerConfig struct {
	// 监听地址，如 0.0.0.0
	Host string `yaml:"host"`
	// 监听端口，如 9091
	Port int `yaml:"port"`
	// RPC 验证用户名（可选）
	Username string `yaml:"username"`
	// RPC 验证密码（可选）
	Password string `yaml:"password"`
}

type DatabaseConfig struct {
	// SQLite 数据文件路径
	FilePath string `yaml:"file_path"`
}

type BiglyBTConfig struct {
	// BiglyBT 的 Transmission RPC 接口地址，如 http://127.0.0.1:9092/transmission/rpc
	RPCURL string `yaml:"rpc_url"`
	// 认证用户名
	Username string `yaml:"username"`
	// 认证密码。只给代理自己下发任务和崩溃恢复使用，不能代替客户端这次请求的 Basic。
	Password string `yaml:"password"`
}

type SonarrConfig struct {
	// Sonarr 访问基础地址，如 http://127.0.0.1:8989
	BaseURL string `yaml:"base_url"`
	// Sonarr API Key
	APIKey string `yaml:"api_key"`
	// 查询超时时间（秒）
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

type PathRulesConfig struct {
	// 默认物理下载目录（无规则匹配时的保底）
	DefaultDownloadDir string `yaml:"default_download_dir"`
	// 磁盘映射规则列表：根据目标目录 B 匹配对应同盘的下载目录 A
	DiskMappings []DiskMapping `yaml:"disk_mappings"`
	// Docker 路径伪装转译规则：宿主机真实路径与 Sonarr 容器内路径映射
	DockerMappings []DockerMapping `yaml:"docker_mappings"`
}

// 单个物理磁盘对应关系（用于确保 Hardlink 处于同一个文件系统）
type DiskMapping struct {
	// 媒体库前缀（目录 B 的前缀），例如 /mnt/disk1/Media
	MediaPrefix string `yaml:"media_prefix"`
	// 对应的同盘下载目录（目录 A），例如 /mnt/disk1/Downloads
	DownloadDir string `yaml:"download_dir"`
}

// 路径伪装关系
type DockerMapping struct {
	// 宿主机真实物理路径前缀
	HostPath string `yaml:"host_path"`
	// Sonarr 容器内部对应的挂载路径前缀
	ContainerPath string `yaml:"container_path"`
}

// 从指定路径加载 YAML 配置文件
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 YAML 配置失败: %w", err)
	}

	// 设定默认值
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 9091
	}
	if cfg.Database.FilePath == "" {
		cfg.Database.FilePath = "proxy_data.db"
	}
	if cfg.Sonarr.TimeoutSeconds == 0 {
		cfg.Sonarr.TimeoutSeconds = 10
	}

	return &cfg, nil
}
