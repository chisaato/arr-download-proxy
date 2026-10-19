package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"arr-download-proxy/internal/model"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

// 初始化 SQLite 数据库并开启 WAL 模式
func NewStore(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败: %w", err)
	}

	// 开启 WAL 预写日志与同步级别，最大化保证断电不丢数据
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA busy_timeout = 5000;",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("执行 pragma 失败 (%s): %w", pragma, err)
		}
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		return nil, err
	}

	return s, nil
}

// 创建数据表结构
func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS downloads (
		info_hash           TEXT PRIMARY KEY,
		name                TEXT,
		raw_torrent         BLOB,
		magnet_url          TEXT,
		state               TEXT NOT NULL,
		series_title        TEXT,
		dest_series_path    TEXT,
		actual_download_dir TEXT,
		docker_mapped_dir   TEXT,
		category            TEXT,
		error_message       TEXT,
		created_at          DATETIME,
		updated_at          DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_downloads_state ON downloads(state);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("初始化表结构失败: %w", err)
	}
	return nil
}

// 保存新接收到的任务（天然幂等）
// 如果已存在相同的 InfoHash，则返回已存在的记录，避免重复处理
func (s *Store) SaveOrGetTask(task *model.DownloadTask) (existing *model.DownloadTask, isNew bool, err error) {
	// 尝试先查询
	found, err := s.GetTask(task.InfoHash)
	if err != nil {
		return nil, false, err
	}
	if found != nil {
		// 已经存在，幂等直接返回原有记录
		return found, false, nil
	}

	now := time.Now()
	task.CreatedAt = now
	task.UpdatedAt = now

	query := `
	INSERT INTO downloads (
		info_hash, name, raw_torrent, magnet_url, state,
		series_title, dest_series_path, actual_download_dir,
		docker_mapped_dir, category, error_message, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err = s.db.Exec(
		query,
		task.InfoHash, task.Name, task.RawTorrent, task.MagnetURL, task.State,
		task.SeriesTitle, task.DestSeriesPath, task.ActualDownloadDir,
		task.DockerMappedDir, task.Category, task.ErrorMessage, task.CreatedAt, task.UpdatedAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("保存任务到 SQLite 失败: %w", err)
	}

	return task, true, nil
}

// 根据 Hash 查询任务
func (s *Store) GetTask(infoHash string) (*model.DownloadTask, error) {
	query := `
	SELECT info_hash, name, raw_torrent, magnet_url, state,
	       series_title, dest_series_path, actual_download_dir,
	       docker_mapped_dir, category, error_message, created_at, updated_at
	FROM downloads WHERE info_hash = ?;
	`
	row := s.db.QueryRow(query, infoHash)

	var t model.DownloadTask
	var rawTorrent sql.NullString
	var magnetURL sql.NullString
	var seriesTitle sql.NullString
	var destSeriesPath sql.NullString
	var actualDownloadDir sql.NullString
	var dockerMappedDir sql.NullString
	var category sql.NullString
	var errorMessage sql.NullString

	err := row.Scan(
		&t.InfoHash, &t.Name, &rawTorrent, &magnetURL, &t.State,
		&seriesTitle, &destSeriesPath, &actualDownloadDir,
		&dockerMappedDir, &category, &errorMessage, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询任务失败: %w", err)
	}

	if rawTorrent.Valid {
		t.RawTorrent = []byte(rawTorrent.String)
	}
	t.MagnetURL = magnetURL.String
	t.SeriesTitle = seriesTitle.String
	t.DestSeriesPath = destSeriesPath.String
	t.ActualDownloadDir = actualDownloadDir.String
	t.DockerMappedDir = dockerMappedDir.String
	t.Category = category.String
	t.ErrorMessage = errorMessage.String

	return &t, nil
}

// 更新任务路径和状态
func (s *Store) UpdateTaskResolved(infoHash string, state model.TaskState, seriesTitle, destPath, actualDir, dockerDir string) error {
	now := time.Now()
	query := `
	UPDATE downloads
	SET state = ?, series_title = ?, dest_series_path = ?, actual_download_dir = ?, docker_mapped_dir = ?, updated_at = ?
	WHERE info_hash = ?;
	`
	_, err := s.db.Exec(query, state, seriesTitle, destPath, actualDir, dockerDir, now, infoHash)
	if err != nil {
		return fmt.Errorf("更新任务状态失败: %w", err)
	}
	return nil
}

// 更新状态为已完成或失败
func (s *Store) UpdateTaskState(infoHash string, state model.TaskState, errMsg string) error {
	now := time.Now()
	query := `
	UPDATE downloads
	SET state = ?, error_message = ?, updated_at = ?
	WHERE info_hash = ?;
	`
	_, err := s.db.Exec(query, state, errMsg, now, infoHash)
	if err != nil {
		return fmt.Errorf("更新任务状态失败: %w", err)
	}
	return nil
}

// 获取所有待恢复的任务（用于意外重启后崩溃自愈）
func (s *Store) ListPendingTasks() ([]*model.DownloadTask, error) {
	query := `
	SELECT info_hash, name, raw_torrent, magnet_url, state,
	       series_title, dest_series_path, actual_download_dir,
	       docker_mapped_dir, category, error_message, created_at, updated_at
	FROM downloads
	WHERE state IN ('RECEIVED', 'READY');
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("查询未决任务失败: %w", err)
	}
	defer rows.Close()

	var tasks []*model.DownloadTask
	for rows.Next() {
		var t model.DownloadTask
		var rawTorrent sql.NullString
		var magnetURL sql.NullString
		var seriesTitle sql.NullString
		var destSeriesPath sql.NullString
		var actualDownloadDir sql.NullString
		var dockerMappedDir sql.NullString
		var category sql.NullString
		var errorMessage sql.NullString

		if err := rows.Scan(
			&t.InfoHash, &t.Name, &rawTorrent, &magnetURL, &t.State,
			&seriesTitle, &destSeriesPath, &actualDownloadDir,
			&dockerMappedDir, &category, &errorMessage, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if rawTorrent.Valid {
			t.RawTorrent = []byte(rawTorrent.String)
		}
		t.MagnetURL = magnetURL.String
		t.SeriesTitle = seriesTitle.String
		t.DestSeriesPath = destSeriesPath.String
		t.ActualDownloadDir = actualDownloadDir.String
		t.DockerMappedDir = dockerMappedDir.String
		t.Category = category.String
		t.ErrorMessage = errorMessage.String

		tasks = append(tasks, &t)
	}
	return tasks, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
