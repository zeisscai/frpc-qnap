package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeisscai/frpc-qnap/internal/config"
	"github.com/zeisscai/frpc-qnap/internal/frpc"
	"github.com/zeisscai/frpc-qnap/internal/supervisor"
	"github.com/zeisscai/frpc-qnap/internal/webui"
)

// Options 是启动管理器的外部参数。
type Options struct {
	DataDir    string // 数据目录（配置、日志）
	FrpcBin    string // frpc 可执行文件路径
	Dev        bool   // 开发模式：前端资源从磁盘读取
	Version    string // manager 版本号
	ListenAddr string
	AdminPort  int // frpc 管理接口端口，0 表示自动选择
}

// Paths 汇总所有运行时路径，避免各处硬编码。
type Paths struct {
	ConfigJSON string
	FrpcTOML   string
	FrpcLog    string
	StateJSON  string
	AssetsDir  string
}

type persistedState struct {
	PasswordHash  string `json:"passwordHash"`
	AdminUser     string `json:"adminUser"`
	AdminPassword string `json:"adminPassword"`
	AdminPort     int    `json:"adminPort"`
}

// Server 是管理器的核心，聚合配置存储、进程守护、frpc API 与 HTTP 服务。
type Server struct {
	opts    Options
	paths   Paths
	store   *config.Store
	sup     *supervisor.Supervisor
	api     *frpc.Client
	auth    *Auth
	admin   config.Admin
	state   persistedState
	version string
	startAt time.Time

	verOnce sync.Once
	frpcVer string
}

// New 初始化管理器：准备目录、加载配置、生成缺失的随机口令。
func New(opts Options) (*Server, error) {
	paths := Paths{
		ConfigJSON: filepath.Join(opts.DataDir, "config", "manager.json"),
		FrpcTOML:   filepath.Join(opts.DataDir, "config", "frpc.toml"),
		FrpcLog:    filepath.Join(opts.DataDir, "logs", "frpc.log"),
		StateJSON:  filepath.Join(opts.DataDir, "config", "state.json"),
		AssetsDir:  filepath.Join("internal", "webui", "assets"),
	}
	for _, dir := range []string{filepath.Dir(paths.ConfigJSON), filepath.Dir(paths.FrpcLog)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建目录 %s 失败：%w", dir, err)
		}
	}

	state, err := loadState(paths.StateJSON)
	if err != nil {
		return nil, err
	}

	// frpc 的内置管理接口始终存在，但只绑定回环并加随机口令。
	if state.AdminUser == "" {
		state.AdminUser = "admin"
	}
	if state.AdminPassword == "" {
		pw, err := randomToken(18)
		if err != nil {
			return nil, err
		}
		state.AdminPassword = pw
	}
	// frpc 的管理接口端口必须在回环上可用；7400 被占用时自动顺延，
	// 避免与其它 frpc / 应用冲突。
	if state.AdminPort == 0 {
		state.AdminPort = opts.AdminPort
	}
	if state.AdminPort == 0 || !portFree(state.AdminPort) {
		state.AdminPort = pickFreePort(7400)
	}

	firstRun := false
	if state.PasswordHash == "" {
		pw := RandomPassword(12)
		hash, err := HashPassword(pw)
		if err != nil {
			return nil, err
		}
		state.PasswordHash = hash
		firstRun = true
		pwFile := filepath.Join(opts.DataDir, "initial-password.txt")
		note := "frpc-qnap 初始登录口令：" + pw + "\n登录后请立即在「设置」中修改。\n"
		if err := config.WriteFileAtomic(pwFile, []byte(note), 0o600); err != nil {
			return nil, err
		}
		log.Printf("首次启动，已生成初始口令：%s（同时写入 %s）", pw, pwFile)
	}

	if err := saveState(paths.StateJSON, state); err != nil {
		return nil, err
	}

	store, err := config.NewStore(paths.ConfigJSON)
	if err != nil {
		return nil, err
	}
	if firstRun {
		// 首次启动写一份默认配置，方便用户直接打开界面。
		_ = store.Save(store.Get())
	}

	admin := config.Admin{
		Addr:     "127.0.0.1",
		Port:     state.AdminPort,
		User:     state.AdminUser,
		Password: state.AdminPassword,
	}

	s := &Server{
		opts:    opts,
		paths:   paths,
		store:   store,
		sup:     supervisor.New(opts.FrpcBin, paths.FrpcTOML, paths.FrpcLog),
		api:     frpc.New(net.JoinHostPort(admin.Addr, strconv.Itoa(admin.Port)), admin.User, admin.Password),
		auth:    NewAuth(state.PasswordHash),
		admin:   admin,
		state:   state,
		version: opts.Version,
		startAt: time.Now(),
	}
	return s, nil
}

// Bootstrap 生成 frpc.toml（若缺失或与 manager.json 不一致）并启动 frpc。
func (s *Server) Bootstrap() error {
	cfg := s.store.Get()
	if cfg.ServerAddr == "" {
		log.Printf("尚未配置服务器地址，frpc 暂不启动（可在网页端完成配置）")
		return nil
	}
	if err := s.writeFrpcTOML(cfg); err != nil {
		return err
	}
	return s.sup.Start()
}

// Shutdown 停止 frpc 子进程。
func (s *Server) Shutdown() {
	s.sup.Stop(15 * time.Second)
}

// Handler 组装完整的 HTTP 路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/proxies", s.handleProxies)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/service/{action}", s.handleService)
	mux.HandleFunc("POST /api/reload", s.handleReload)
	mux.HandleFunc("GET /api/logs", s.handleLogs)

	mux.Handle("/", s.staticHandler())

	return stripPrefix(s.securityHeaders(s.requireAuth(mux)))
}

// ---------- 中间件 ----------

// stripPrefix 兼容 QTS 反向代理的两种行为：保留或剥离 /frpc-qnap 前缀。
func stripPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/frpc-qnap" {
			// 补上尾斜杠，否则前端相对路径会解析到 QTS 根目录。
			http.Redirect(w, r, "/frpc-qnap/", http.StatusFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/frpc-qnap") {
			p := strings.TrimPrefix(r.URL.Path, "/frpc-qnap")
			if p == "" {
				p = "/"
			}
			r.URL.Path = p
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		// 允许被 QTS 桌面同源 iframe 嵌入；禁止被外部站点嵌套。
		h.Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}

// requireAuth 只保护 /api 下的接口，静态资源与登录页放行。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	open := map[string]bool{
		"/api/health":  true,
		"/api/login":   true,
		"/api/session": true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || open[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || !s.auth.Valid(c.Value) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话已过期"})
			return
		}
		// 写操作要求自定义请求头，配合 SameSite Cookie 抵御 CSRF。
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "frpc-qnap" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "缺少 CSRF 校验头"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- 静态资源 ----------

func (s *Server) staticHandler() http.Handler {
	var fsys fs.FS
	if s.opts.Dev {
		fsys = os.DirFS(s.paths.AssetsDir)
	} else {
		fsys = webui.FS()
	}
	fileServer := http.FileServerFS(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// hash 路由：入口统一返回 index.html。
		// 注意不能把 URL 改写成 /index.html，否则 FileServer 会 301 回 /。
		if r.URL.Path == "/" || r.URL.Path == "" {
			http.ServeFileFS(w, r, fsys, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ---------- API ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	authed := false
	if c, err := r.Cookie(sessionCookie); err == nil {
		authed = s.auth.Valid(c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": authed,
		"hasPassword":   s.auth.HasPassword(),
		"version":       s.version,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	token, err := s.auth.Login(req.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL / time.Second),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"protocols":      config.Protocols,
		"logLevels":      config.LogLevels,
		"proxyTypes":     config.ProxyTypes,
		"managerVersion": s.version,
		"frpcVersion":    s.frpcVersion(),
		"goVersion":      runtime.Version(),
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	st := s.sup.State()
	cfg := s.store.Get()

	type counts struct {
		Total   int `json:"total"`
		Running int `json:"running"`
		Error   int `json:"error"`
	}
	c := counts{Total: len(cfg.Proxies)}

	adminOnline := false
	statusList, err := s.api.Status(ctx)
	if err == nil {
		adminOnline = true
		for _, group := range statusList {
			for _, p := range group {
				// frpc 的状态值不止 "error"，还有 "start error" 这类组合值。
				switch {
				case p.Status == "running":
					c.Running++
				case strings.Contains(p.Status, "error"):
					c.Error++
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":       s.version,
		"frpcVersion":   s.frpcVersion(),
		"process":       st,
		"adminOnline":   adminOnline,
		"server":        map[string]any{"addr": cfg.ServerAddr, "port": cfg.ServerPort, "protocol": cfg.Protocol},
		"counts":        c,
		"uptimeSeconds": int(time.Since(s.startAt).Seconds()),
	})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	tokenSet := cfg.AuthToken != ""
	cfg.AuthToken = "" // 永不把 token 回传给浏览器
	// 隧道级密钥同样不回显；前端留空表示"保持不变"。
	for i := range cfg.Proxies {
		cfg.Proxies[i].SecretKey = ""
		cfg.Proxies[i].HTTPPassword = ""
	}
	writeJSON(w, http.StatusOK, struct {
		config.Config
		TokenSet bool `json:"tokenSet"`
	}{Config: cfg, TokenSet: tokenSet})
}

// proxyView 是隧道配置与运行时状态的合并视图。
type proxyView struct {
	config.Proxy
	Status     string `json:"status"`
	RemoteAddr string `json:"remoteAddr,omitempty"`
	Err        string `json:"err,omitempty"`
}

func (s *Server) handleProxies(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	live := map[string]frpc.ProxyStatus{}
	if st, err := s.api.Status(ctx); err == nil {
		for _, group := range st {
			for _, p := range group {
				live[p.Name] = p
			}
		}
	}

	out := make([]proxyView, 0, len(cfg.Proxies))
	for _, p := range cfg.Proxies {
		v := proxyView{Proxy: p, Status: "unknown"}
		v.SecretKey = ""
		v.HTTPPassword = ""
		if lp, ok := live[p.Name]; ok {
			v.Status = lp.Status
			v.RemoteAddr = lp.RemoteAddr
			v.Err = lp.Err
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxies": out, "pid": s.sup.State().Pid})
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var incoming config.Config
	if err := decodeJSON(r, &incoming); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	old := s.store.Get()
	// 空 token 表示"保持不变"，避免前端无法回显密钥时误清空。
	if incoming.AuthToken == "" && old.AuthToken != "" {
		incoming.AuthToken = old.AuthToken
	}
	mergeProxySecrets(old, &incoming)

	result, err := s.applyConfig(old, incoming)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type applyResult struct {
	Restarted bool   `json:"restarted"`
	Reloaded  bool   `json:"reloaded"`
	Started   bool   `json:"started"`
	Message   string `json:"message"`
}

// mergeProxySecrets 让"留空"的密钥字段沿用旧值，避免前端无法回显时被清空。
func mergeProxySecrets(old config.Config, next *config.Config) {
	prev := make(map[string]config.Proxy, len(old.Proxies))
	for _, p := range old.Proxies {
		prev[p.Name] = p
	}
	for i := range next.Proxies {
		o, ok := prev[next.Proxies[i].Name]
		if !ok {
			continue
		}
		if next.Proxies[i].SecretKey == "" {
			next.Proxies[i].SecretKey = o.SecretKey
		}
		if next.Proxies[i].HTTPPassword == "" {
			next.Proxies[i].HTTPPassword = o.HTTPPassword
		}
	}
}

// applyConfig 是配置生效的完整链路：校验 → 生成 TOML → 落盘 → 热重载或重启。
func (s *Server) applyConfig(old, next config.Config) (applyResult, error) {
	var res applyResult

	if err := next.Validate(); err != nil {
		return res, err
	}
	tomlText := next.ToTOML(s.admin, s.paths.FrpcLog)
	if err := s.verifyTOML(tomlText); err != nil {
		return res, err
	}

	// 先落 manager.json（唯一真相源），再写 frpc.toml。
	if err := s.store.Save(next); err != nil {
		return res, err
	}
	if err := s.writeFrpcTOML(next); err != nil {
		return res, err
	}

	wasConfigured := old.ServerAddr != ""
	if !wasConfigured {
		if next.ServerAddr == "" {
			res.Message = "已保存"
			return res, nil
		}
		if err := s.sup.Start(); err != nil {
			return res, err
		}
		res.Started = true
		res.Message = "配置已保存，frpc 已启动"
		return res, nil
	}

	if config.RequiresRestart(old, next) {
		if err := s.sup.Restart(15 * time.Second); err != nil {
			return res, err
		}
		res.Restarted = true
		res.Message = "配置已保存，frpc 已重启（公共参数不支持热重载）"
		return res, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.api.Reload(ctx); err != nil {
		// 热重载失败时退化为重启，保证配置一定生效。
		if rerr := s.sup.Restart(15 * time.Second); rerr != nil {
			return res, fmt.Errorf("热重载失败（%v），重启也失败：%w", err, rerr)
		}
		res.Restarted = true
		res.Message = fmt.Sprintf("热重载失败（%v），已改为重启生效", err)
		return res, nil
	}
	res.Reloaded = true
	res.Message = "配置已保存并热重载，现有连接未中断"
	return res, nil
}

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	switch action {
	case "start":
		if err := s.sup.Start(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	case "stop":
		s.sup.Stop(15 * time.Second)
	case "restart":
		if err := s.sup.Restart(15 * time.Second); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未知操作：" + action})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": s.sup.State()})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.api.Reload(ctx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			lines = n
		}
	}
	path := s.paths.FrpcLog
	if file == "manager" {
		path = filepath.Join(s.opts.DataDir, "logs", "manager.log")
	}
	content, err := readTail(path, lines)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"content": "", "path": path})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": content, "path": path})
}

// ---------- 内部工具 ----------

func (s *Server) writeFrpcTOML(cfg config.Config) error {
	text := cfg.ToTOML(s.admin, s.paths.FrpcLog)
	if old, err := os.ReadFile(s.paths.FrpcTOML); err == nil && string(old) != text {
		_ = config.WriteFileAtomic(s.paths.FrpcTOML+".bak", old, 0o600)
	}
	return config.WriteFileAtomic(s.paths.FrpcTOML, []byte(text), 0o600)
}

// verifyTOML 调用 frpc 自己的 verify 子命令做权威校验。
// 找不到二进制时退化为本地校验（已在 Validate 中做过）。
func (s *Server) verifyTOML(text string) error {
	if _, err := os.Stat(s.opts.FrpcBin); err != nil {
		return nil
	}
	tmp := filepath.Join(s.opts.DataDir, "config", ".verify.toml")
	if err := config.WriteFileAtomic(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)

	cmd := exec.Command(s.opts.FrpcBin, "verify", "-c", tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("frpc 校验未通过：%s", msg)
	}
	return nil
}

// frpcVersion 惰性执行 frpc -v 并缓存结果（overview 会被频繁轮询）。
func (s *Server) frpcVersion() string {
	s.verOnce.Do(func() {
		if _, err := os.Stat(s.opts.FrpcBin); err != nil {
			return
		}
		out, err := exec.Command(s.opts.FrpcBin, "-v").Output()
		if err != nil {
			return
		}
		v := strings.TrimSpace(string(out))
		v = strings.TrimPrefix(v, "frpc version ")
		if len(v) > 32 {
			v = v[:32]
		}
		s.frpcVer = v
	})
	return s.frpcVer
}

// portFree 检查回环端口是否可绑定。
func portFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// pickFreePort 从 base 开始找一个可用端口。
func pickFreePort(base int) int {
	for p := base; p < base+20; p++ {
		if portFree(p) {
			return p
		}
	}
	return base
}

func loadState(path string) (persistedState, error) {
	var st persistedState
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("解析 state.json 失败：%w", err)
	}
	return st, nil
}

func saveState(path string, st persistedState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("请求格式错误：%w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// readTail 读取文件末尾最多 maxLines 行，最多读 256KB。
func readTail(path string, maxLines int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const maxBytes = 256 << 10
	size := st.Size()
	offset := int64(0)
	length := size
	if length > maxBytes {
		length = maxBytes
		offset = size - length
	}
	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return "", err
	}

	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n"), nil
}
