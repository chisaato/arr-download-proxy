package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"arr-download-proxy/internal/config"
	"arr-download-proxy/internal/downloader"
	"arr-download-proxy/internal/logsafe"
	"arr-download-proxy/internal/model"
	"arr-download-proxy/internal/pathmapper"
	"arr-download-proxy/internal/server"
	"arr-download-proxy/internal/sonarr"
	"arr-download-proxy/internal/store"

	"github.com/spf13/cobra"
)

type cliOptions struct {
	configPath string
	debug      bool
}

func main() {
	cmd := newRootCommand(serve)
	if err := executeRoot(cmd, os.Args[1:]); err != nil {
		// 用法和参数错误沿用 flag 的退出码。配置或服务失败仍由 log.Fatalf 以 1 退出。
		os.Exit(2)
	}
}

func newRootCommand(runFn func(cliOptions) error) *cobra.Command {
	opts := &cliOptions{}
	cmd := &cobra.Command{
		Use:   "arr-download-proxy",
		Short: "在 Sonarr 与 BiglyBT 之间转发下载任务",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFn(*opts)
		},
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.InitDefaultHelpFlag()
	cmd.Flags().StringVarP(&opts.configPath, "config", "c", "config.yaml", "配置文件路径")
	cmd.Flags().BoolVar(&opts.debug, "debug", false, "输出每次轮询等明细日志；默认只在 torrent-get 数量或路径转译变化时记录")
	return cmd
}

func executeRoot(cmd *cobra.Command, args []string) error {
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	return cmd.Execute()
}

func serve(opts cliOptions) error {
	logsafe.SetDebug(opts.debug)

	// 1. 读取配置文件
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		log.Printf("[提示] 加载 %s 失败 (%v)，尝试加载 config.example.yaml 作为默认模版...", opts.configPath, err)
		cfg, err = config.Load("config.example.yaml")
		if err != nil {
			log.Fatalf("[致命错误] 无法加载任何配置文件: %v", err)
		}
	}

	// 2. 初始化持久化存储 (SQLite + WAL)
	db, err := store.NewStore(cfg.Database.FilePath)
	if err != nil {
		log.Fatalf("[致命错误] 初始化存储层失败: %v", err)
	}
	defer db.Close()
	absDB := cfg.Database.FilePath
	if abs, err := filepath.Abs(cfg.Database.FilePath); err == nil {
		absDB = abs
	}
	log.Printf("[存储就绪] SQLite 数据库已连接并开启 WAL 模式: %s", absDB)

	// 3. 初始化核心组件
	bbtClient := downloader.NewBiglyBTClient(cfg.BiglyBT)
	sonarrCli := sonarr.NewClient(cfg.Sonarr)
	pathMapper := pathmapper.NewPathMapper(cfg.PathRules)

	// 4. 执行崩溃自愈与状态恢复例程 (Crash Recovery)
	performCrashRecovery(db, bbtClient, pathMapper)

	// 5. 创建 HTTP 代理服务
	srvHandler := server.NewServer(cfg, db, bbtClient, sonarrCli, pathMapper)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: srvHandler.Handler(),
	}

	// 6. 异步启动服务
	go func() {
		log.Printf("=====================================================")
		log.Printf("  Arr-Download-Proxy 已启动，正在监听: %s", addr)
		log.Printf("  Transmission RPC 伪装端点: http://%s/transmission/rpc", addr)
		log.Printf("  Sonarr Webhook 接收端点:   http://%s/webhook/sonarr", addr)
		log.Printf("=====================================================")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[致命错误] HTTP 服务异常退出: %v", err)
		}
	}()

	// 7. 优雅关机监听
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[停机] 正在平滑关闭代理服务...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("[警告] 强制关闭服务: %v", err)
	}
	log.Println("[退出] 服务已成功停止。")
	return nil
}

// 崩溃自愈：检查因重启或意外断电残留的未完成任务并重放
func performCrashRecovery(db *store.Store, bbt *downloader.BiglyBTClient, mapper *pathmapper.PathMapper) {
	tasks, err := db.ListPendingTasks()
	if err != nil {
		log.Printf("[自愈检查] 获取待恢复任务失败: %v", err)
		return
	}

	if len(tasks) == 0 {
		log.Println("[自愈检查] 无未决任务，状态正常。")
		return
	}

	log.Printf("[自愈检查] 发现 %d 个未决任务，开始执行自愈投递...", len(tasks))
	for _, t := range tasks {
		targetDir := t.ActualDownloadDir
		if targetDir == "" {
			targetDir = mapper.ResolveDownloadDir(t.DestSeriesPath)
		}

		// 重新向 BiglyBT 投递
		err := bbt.AddTorrent(targetDir, t.RawTorrent, t.MagnetURL, false)
		if err != nil {
			log.Printf("[自愈投递失败] Hash: %s, 原因: %s", t.InfoHash, logsafe.Error(err))
		} else {
			log.Printf("[自愈投递成功] Hash: %s 成功恢复并送达 BiglyBT", t.InfoHash)
			_ = db.UpdateTaskState(t.InfoHash, model.StateDownloading, "")
		}
	}
}
