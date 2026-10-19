package torrent

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// 解析结果结构体
type ParseResult struct {
	// 40位十六进制小写哈希
	InfoHash string
	// 种子名称
	Name string
	// 原始种子数据
	RawBytes []byte
	// 磁力链接地址（如果是以 Magnet 方式添加）
	MagnetURL string
}

// 解析 Base64 编码的 Torrent 文件内容
func ParseTorrentBase64(b64Content string) (*ParseResult, error) {
	rawBytes, err := base64.StdEncoding.DecodeString(b64Content)
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败: %w", err)
	}

	return ParseTorrentBytes(rawBytes)
}

// 解析 Torrent 原始字节数据
func ParseTorrentBytes(rawBytes []byte) (*ParseResult, error) {
	reader := bytes.NewReader(rawBytes)
	mi, err := metainfo.Load(reader)
	if err != nil {
		return nil, fmt.Errorf("解析 metainfo 结构失败: %w", err)
	}

	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, fmt.Errorf("解构 Info 字典失败: %w", err)
	}

	// 转换为 40 位小写十六进制字符串
	infoHash := mi.HashInfoBytes().HexString()
	infoHash = strings.ToLower(infoHash)

	return &ParseResult{
		InfoHash: infoHash,
		Name:     info.Name,
		RawBytes: rawBytes,
	}, nil
}

// 解析磁力链接 (magnet:?xt=urn:btih:...)
func ParseMagnetURL(magnetStr string) (*ParseResult, error) {
	m, err := metainfo.ParseMagnetUri(magnetStr)
	if err != nil {
		return nil, fmt.Errorf("解析磁力链接失败: %w", err)
	}

	// 检查 InfoHash 是否有效
	if m.InfoHash == (metainfo.Hash{}) {
		return nil, errors.New("磁力链接未包含有效的 xt=urn:btih 标识")
	}

	infoHash := strings.ToLower(m.InfoHash.HexString())
	displayName := m.DisplayName
	if displayName == "" {
		displayName = infoHash
	}

	return &ParseResult{
		InfoHash:  infoHash,
		Name:      displayName,
		MagnetURL: magnetStr,
	}, nil
}
