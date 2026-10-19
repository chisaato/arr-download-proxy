package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"arr-download-proxy/internal/config"
	"arr-download-proxy/internal/downloader"
	"arr-download-proxy/internal/logsafe"
	"arr-download-proxy/internal/model"
	"arr-download-proxy/internal/pathmapper"
	"arr-download-proxy/internal/sonarr"
	"arr-download-proxy/internal/store"
	"arr-download-proxy/internal/torrent"
)

type Server struct {
	cfg        *config.Config
	store      *store.Store
	bbtClient  *downloader.BiglyBTClient
	sonarrCli  *sonarr.Client
	pathMapper *pathmapper.PathMapper
	mux        *http.ServeMux

	mu               sync.Mutex
	seenSession      bool
	seenTorrentGet   bool
	lastTorrentCount int
	lastTranslated   int
	authTimeout      time.Duration
}

func NewServer(
	cfg *config.Config,
	store *store.Store,
	bbtClient *downloader.BiglyBTClient,
	sonarrCli *sonarr.Client,
	pathMapper *pathmapper.PathMapper,
) *Server {
	s := &Server{
		cfg:        cfg,
		store:      store,
		bbtClient:  bbtClient,
		sonarrCli:  sonarrCli,
		pathMapper: pathMapper,
		mux:        http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// 针对 Transmission RPC 协议的主端点
	s.mux.HandleFunc("/transmission/rpc", s.handleTransmissionRPC)
	// Sonarr On Grab Webhook 接收端点
	s.mux.HandleFunc("/webhook/sonarr", s.handleSonarrWebhook)
	// 健康检查
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
}

const transmissionChallenge = `Basic realm="transmission"`

// 处理 Transmission RPC 协议
func (s *Server) handleTransmissionRPC(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	switch r.Method {
	case http.MethodGet:
		if _, ok := r.URL.Query()["json"]; ok {
			raw := r.URL.Query().Get("json")
			if strings.TrimSpace(raw) == "" {
				log.Printf("[RPC] method=GET result=invalid-json elapsed=%s", elapsed(start))
				http.Error(w, "json query is empty", http.StatusBadRequest)
				return
			}
			s.finishRPC(w, r, []byte(raw), start)
			return
		}
		s.handleHandshake(w, r, start)
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[RPC] result=bad-request elapsed=%s err=%s", elapsed(start), logsafe.Error(err))
			http.Error(w, "无法读取请求体", http.StatusBadRequest)
			return
		}
		s.finishRPC(w, r, body, start)
	default:
		log.Printf("[RPC] method=%s result=method-not-allowed elapsed=%s", r.Method, elapsed(start))
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleHandshake(w http.ResponseWriter, r *http.Request, start time.Time) {
	user, pass, ok := r.BasicAuth()
	status, upstream, retries, sessionID, err := downloader.Handshake(
		r.Context(),
		s.cfg.BiglyBT.RPCURL,
		user,
		pass,
		ok,
		r.Header.Get("X-Transmission-Session-Id"),
		s.authTimeout,
	)
	reason := handshakeReason(status, err)
	log.Printf("[AUTH] stage=handshake result=%s upstream=%d retry=%d elapsed=%s", reason, upstream, retries, elapsed(start))
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", transmissionChallenge)
	}
	if status == http.StatusConflict && sessionID != "" {
		w.Header().Set("X-Transmission-Session-Id", sessionID)
	}
	w.WriteHeader(status)
}

func (s *Server) finishRPC(w http.ResponseWriter, r *http.Request, body []byte, start time.Time) {
	user, pass, ok := r.BasicAuth()
	scoped, err := downloader.OpenScoped(r.Context(), s.cfg.BiglyBT.RPCURL, user, pass, ok, s.authTimeout)
	if err != nil {
		s.writeAuthFailure(w, err, start)
		return
	}
	upstream, retries := scoped.Trace()
	log.Printf("[AUTH] stage=verify result=ok upstream=%d retry=%d elapsed=%s", upstream, retries, elapsed(start))

	var req downloader.RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		log.Printf("[RPC] result=invalid-json elapsed=%s", elapsed(start))
		http.Error(w, "invalid rpc json", http.StatusBadRequest)
		return
	}
	var rawEnv struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(body, &rawEnv)

	switch req.Method {
	case "torrent-add":
		s.handleTorrentAdd(w, req, start)
	case "torrent-get":
		s.handleTorrentGet(w, r, req, rawEnv.Arguments, start, scoped)
	case "session-get":
		s.handleSessionGet(w, r, req, rawEnv.Arguments, start, scoped)
	default:
		// 未列入白名单的方法仍返回合成成功，避免本轮扩大透传面。只记录方法名。
		log.Printf("[RPC] method=%s result=synthetic-success forward=no reason=not-whitelisted elapsed=%s", req.Method, elapsed(start))
		s.respondRPC(w, req.Tag, "success", map[string]interface{}{})
	}
}

// 核心逻辑：拦截 Sonarr 的添加种子请求
func (s *Server) handleTorrentAdd(w http.ResponseWriter, req downloader.RPCRequest, start time.Time) {
	var metaInfo string
	var filename string
	paused := false

	if v, ok := req.Arguments["metainfo"].(string); ok {
		metaInfo = v
	}
	if v, ok := req.Arguments["filename"].(string); ok {
		filename = v
	}
	if v, ok := req.Arguments["paused"].(bool); ok {
		paused = v
	}

	var parseRes *torrent.ParseResult
	var parseErr error

	if metaInfo != "" {
		parseRes, parseErr = torrent.ParseTorrentBase64(metaInfo)
	} else if filename != "" {
		parseRes, parseErr = torrent.ParseMagnetURL(filename)
	} else {
		log.Printf("[RPC] method=torrent-add result=missing-payload elapsed=%s", elapsed(start))
		s.respondRPC(w, req.Tag, "missing metainfo or filename", nil)
		return
	}

	if parseErr != nil {
		log.Printf("[RPC] method=torrent-add result=invalid-torrent elapsed=%s err=%s", elapsed(start), logsafe.Error(parseErr))
		s.respondRPC(w, req.Tag, "invalid torrent data", nil)
		return
	}

	// 1. 先落盘保存到 SQLite (WAL模式)，具备天然幂等性与意外防丢
	task := &model.DownloadTask{
		InfoHash:   parseRes.InfoHash,
		Name:       parseRes.Name,
		RawTorrent: parseRes.RawBytes,
		MagnetURL:  parseRes.MagnetURL,
		State:      model.StateReceived,
	}

	existing, isNew, err := s.store.SaveOrGetTask(task)
	queueState := model.StateReceived
	if err != nil {
		log.Printf("[队列] hash=%s 新任务=false 状态=持久化失败 err=%s", task.InfoHash, logsafe.Error(err))
	} else if !isNew {
		queueState = existing.State
		log.Printf("[队列] hash=%s 新任务=false 状态=%s 标题=%s", existing.InfoHash, existing.State, existing.Name)
	} else {
		log.Printf("[队列] hash=%s 新任务=true 状态=%s 标题=%s", task.InfoHash, task.State, task.Name)
	}

	// 2. 破除时序陷阱：立即向 Sonarr 返回成功，让 Sonarr 得以完成事件发布与数据库写入
	respArgs := map[string]interface{}{
		"torrent-added": map[string]interface{}{
			"id":         1,
			"hashString": parseRes.InfoHash,
			"name":       parseRes.Name,
		},
	}
	s.respondRPC(w, req.Tag, "success", respArgs)
	log.Printf("[RPC] method=torrent-add result=ok elapsed=%s hash=%s 状态=%s", elapsed(start), parseRes.InfoHash, queueState)

	// 3. 异步启动协程补全元数据并下发给 BiglyBT
	go s.resolveAndDispatch(task, paused)
}

// 异步补齐剧集路径并下发 BiglyBT
func (s *Server) resolveAndDispatch(task *model.DownloadTask, paused bool) {
	log.Printf("[路径] hash=%s 来源=sonarr-api 开始检索", task.InfoHash)

	// 稍作微小延迟以确保 Sonarr 数据库写入完成，随后重试查询
	seriesTitle, destPath, err := s.sonarrCli.QuerySeriesPathWithRetry(task.InfoHash, 5, 600*time.Millisecond)

	var downloadDir string
	if err != nil {
		// 若主动查询超时或未拿到（可能依赖 Webhook），使用默认目录保底
		downloadDir = s.pathMapper.ResolveDownloadDir("")
		log.Printf("[路径] hash=%s 来源=兜底 原因=%s 宿主下载=%s", task.InfoHash, logsafe.Error(err), downloadDir)
	} else {
		downloadDir = s.pathMapper.ResolveDownloadDir(destPath)
		log.Printf("[路径] hash=%s 来源=sonarr-api 剧名=%s 目标=%s 宿主下载=%s", task.InfoHash, seriesTitle, destPath, downloadDir)
	}

	dockerDir := s.pathMapper.TranslateToDockerPath(downloadDir)
	if dockerDir != downloadDir {
		log.Printf("[路径] hash=%s 容器回看=%s", task.InfoHash, dockerDir)
	}

	// 更新本地状态为 READY
	_ = s.store.UpdateTaskResolved(task.InfoHash, model.StateReady, seriesTitle, destPath, downloadDir, dockerDir)

	s.dispatchToBBT(task.InfoHash, downloadDir, task.RawTorrent, task.MagnetURL, paused)
}

// 处理 Sonarr On Grab Webhook
func (s *Server) handleSonarrWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload sonarr.GrabWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "解析 Webhook 失败", http.StatusBadRequest)
		return
	}

	if payload.EventType != "Grab" || payload.DownloadId == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	hash := strings.ToLower(payload.DownloadId)
	seriesTitle := ""
	destPath := ""
	if payload.Series != nil {
		seriesTitle = payload.Series.Title
		destPath = payload.Series.Path
	}
	log.Printf("[Webhook] hash=%s 剧名=%s 目标=%s", hash, seriesTitle, destPath)

	task, err := s.store.GetTask(hash)
	if err != nil || task == nil {
		log.Printf("[Webhook] hash=%s 未下发 原因=本地没有对应任务。本代理看不到 Sonarr 内部硬链接", hash)
	} else if task.State != model.StateReceived {
		log.Printf("[Webhook] hash=%s 未下发 原因=任务状态已是 %s。本代理看不到 Sonarr 内部硬链接", hash, task.State)
	} else if destPath == "" {
		log.Printf("[Webhook] hash=%s 未下发 原因=回调没有剧集路径", hash)
	} else {
		downloadDir := s.pathMapper.ResolveDownloadDir(destPath)
		dockerDir := s.pathMapper.TranslateToDockerPath(downloadDir)
		log.Printf("[路径] hash=%s 来源=webhook 剧名=%s 目标=%s 宿主下载=%s", hash, seriesTitle, destPath, downloadDir)
		if dockerDir != downloadDir {
			log.Printf("[路径] hash=%s 容器回看=%s", hash, dockerDir)
		}
		_ = s.store.UpdateTaskResolved(hash, model.StateReady, seriesTitle, destPath, downloadDir, dockerDir)
		s.dispatchToBBT(hash, downloadDir, task.RawTorrent, task.MagnetURL, false)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"accepted"}`))
}

// 处理任务状态查询并进行 Docker 路径转译
func (s *Server) handleTorrentGet(w http.ResponseWriter, r *http.Request, req downloader.RPCRequest, arguments json.RawMessage, start time.Time, src *downloader.ScopedClient) {
	fields, idCount := torrentGetQueryShape(arguments)
	log.Printf("[RPC] method=torrent-get stage=query fields=%s ids=%d filter=none", fields, idCount)
	resp, err := src.CallRaw(r.Context(), "torrent-get", arguments)
	if err != nil {
		log.Printf("[RPC] method=torrent-get result=error elapsed=%s err=%s", elapsed(start), logsafe.Error(err))
		s.respondRPC(w, req.Tag, "failed to get torrents from client", nil)
		return
	}
	rewritten, translated, err := downloader.RewriteTorrentDownloadDirs(resp.Arguments, s.pathMapper.TranslateToDockerPath)
	if err != nil {
		log.Printf("[RPC] method=torrent-get result=bad-upstream elapsed=%s", elapsed(start))
		s.respondRPC(w, req.Tag, "failed to read torrents from client", nil)
		return
	}
	count := torrentCount(rewritten)
	s.logTorrentGet(count, translated, elapsed(start))
	s.respondRPCRaw(w, req.Tag, resp.Result, rewritten)
}

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request, req downloader.RPCRequest, arguments json.RawMessage, start time.Time, src *downloader.ScopedClient) {
	resp, err := src.CallRaw(r.Context(), "session-get", arguments)
	if err != nil {
		log.Printf("[RPC] method=session-get result=error elapsed=%s err=%s", elapsed(start), logsafe.Error(err))
		s.respondRPC(w, req.Tag, "failed to get session from client", nil)
		return
	}
	rewritten, changed, err := downloader.RewriteSessionDownloadDir(resp.Arguments, s.pathMapper.TranslateToDockerPath)
	if err != nil {
		log.Printf("[RPC] method=session-get result=bad-upstream elapsed=%s", elapsed(start))
		s.respondRPC(w, req.Tag, "failed to read session from client", nil)
		return
	}
	s.respondRPCRaw(w, req.Tag, resp.Result, rewritten)
	s.mu.Lock()
	first := !s.seenSession
	s.seenSession = true
	s.mu.Unlock()
	if first || logsafe.Debug() || changed {
		log.Printf("[RPC] method=session-get result=ok download_dir_translated=%t elapsed=%s", changed, elapsed(start))
	}
}

func (s *Server) dispatchToBBT(hash, downloadDir string, rawTorrent []byte, magnetURL string, paused bool) {
	start := time.Now()
	err := s.bbtClient.AddTorrent(downloadDir, rawTorrent, magnetURL, paused)
	if err != nil {
		log.Printf("[BBT] hash=%s 结果=失败 耗时=%s paused=%t 宿主下载=%s err=%s", hash, elapsed(start), paused, downloadDir, logsafe.Error(err))
		_ = s.store.UpdateTaskState(hash, model.StateFailed, logsafe.Clip(err.Error(), 180))
		return
	}
	log.Printf("[BBT] hash=%s 结果=已下发 耗时=%s paused=%t 宿主下载=%s", hash, elapsed(start), paused, downloadDir)
	_ = s.store.UpdateTaskState(hash, model.StateDownloading, "")
	log.Printf("[导入] hash=%s 本代理只确认已交给 BiglyBT，状态=%s；看不到 Sonarr 内部是否硬链接", hash, model.StateDownloading)
}

func (s *Server) logTorrentGet(count, translated int, elapsedText string) {
	s.mu.Lock()
	should := torrentGetShouldLog(logsafe.Debug(), s.seenTorrentGet, s.lastTorrentCount, s.lastTranslated, count, translated)
	s.seenTorrentGet = true
	s.lastTorrentCount = count
	s.lastTranslated = translated
	s.mu.Unlock()
	if should {
		log.Printf("[RPC] method=torrent-get result=ok count=%d translated=%d elapsed=%s", count, translated, elapsedText)
	}
}

func elapsed(start time.Time) string {
	return time.Since(start).Round(time.Millisecond).String()
}

func torrentGetQueryShape(arguments json.RawMessage) (fields string, idCount int) {
	if len(arguments) == 0 || string(arguments) == "null" {
		return "", 0
	}
	var payload struct {
		Fields []string        `json:"fields"`
		IDs    json.RawMessage `json:"ids"`
	}
	if err := json.Unmarshal(arguments, &payload); err != nil {
		return "", 0
	}
	if len(payload.IDs) > 0 && string(payload.IDs) != "null" {
		var many []interface{}
		if err := json.Unmarshal(payload.IDs, &many); err == nil {
			idCount = len(many)
		} else {
			idCount = 1
		}
	}
	return strings.Join(payload.Fields, ","), idCount
}

func torrentCount(arguments json.RawMessage) int {
	var payload struct {
		Torrents []json.RawMessage `json:"torrents"`
	}
	if err := json.Unmarshal(arguments, &payload); err != nil {
		return 0
	}
	return len(payload.Torrents)
}

func (s *Server) respondRPCRaw(w http.ResponseWriter, tag int, result string, arguments json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	resp := downloader.RPCResponse{Result: result, Tag: tag, Arguments: arguments}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) respondRPC(w http.ResponseWriter, tag int, result string, args interface{}) {
	w.Header().Set("Content-Type", "application/json")
	resp := downloader.RPCResponse{
		Result: result,
		Tag:    tag,
	}
	if args != nil {
		raw, _ := json.Marshal(args)
		resp.Arguments = raw
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func authReason(err error) string {
	var ue *downloader.UpstreamError
	if errors.As(err, &ue) && ue.Reason != "" {
		return ue.Reason
	}
	return "upstream"
}

func handshakeReason(status int, err error) string {
	if err != nil {
		return authReason(err)
	}
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusConflict:
		return "challenge"
	default:
		return "ok"
	}
}

func (s *Server) writeAuthFailure(w http.ResponseWriter, err error, start time.Time) {
	status := http.StatusBadGateway
	upstream := 0
	retries := 0
	reason := "upstream"
	var ue *downloader.UpstreamError
	if errors.As(err, &ue) {
		status = ue.Status
		upstream = ue.Upstream
		retries = ue.Retries
		if ue.Reason != "" {
			reason = ue.Reason
		}
	}
	log.Printf("[AUTH] stage=verify result=%s upstream=%d retry=%d elapsed=%s", reason, upstream, retries, elapsed(start))
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", transmissionChallenge)
	}
	http.Error(w, reason, status)
}

func queryKeys(r *http.Request) string {
	values := r.URL.Query()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		s.mux.ServeHTTP(sw, r)
		log.Printf("[HTTP] method=%s path=%s query_keys=%s status=%d remote=%s elapsed=%s",
			r.Method, r.URL.Path, queryKeys(r), sw.status, logsafe.Redact(r.RemoteAddr), elapsed(start))
	})
}
