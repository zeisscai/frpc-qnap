// Package config 负责 frpc 客户端配置的建模、校验与 TOML 生成。
//
// 本项目的设计原则：manager.json 是唯一真相源（结构化 JSON），frpc.toml
// 是每次保存时重新生成的产物。这样后端不需要解析 TOML，也避免用户手工
// 改坏文件后被静默覆盖——原样文件会在保存前备份为 frpc.toml.bak。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// 支持的传输协议与日志级别，顺序即 UI 下拉框顺序。
var (
	Protocols  = []string{"tcp", "kcp", "quic", "websocket", "wss"}
	LogLevels  = []string{"trace", "debug", "info", "warn", "error"}
	ProxyTypes = []string{"tcp", "udp", "http", "https", "stcp", "xtcp", "sudp"}

	// 需要 remotePort 的代理类型。
	remotePortTypes = map[string]bool{"tcp": true, "udp": true}
	// 走域名路由的代理类型。
	domainTypes = map[string]bool{"http": true, "https": true}
	// 点对点加密类型。
	p2pTypes = map[string]bool{"stcp": true, "xtcp": true, "sudp": true}
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Proxy 是一条隧道规则。字段命名对齐 frp v1 (TOML) 配置，便于直接映射；
// 用不到的字段留空即可，生成 TOML 时会跳过。
type Proxy struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	LocalIP           string `json:"localIP"`
	LocalPort         int    `json:"localPort"`
	RemotePort        int    `json:"remotePort,omitempty"`
	CustomDomains     string `json:"customDomains,omitempty"` // 逗号分隔，如 a.example.com,b.example.com
	Subdomain         string `json:"subdomain,omitempty"`
	Locations         string `json:"locations,omitempty"`
	HTTPUser          string `json:"httpUser,omitempty"`
	HTTPPassword      string `json:"httpPassword,omitempty"`
	HostHeaderRewrite string `json:"hostHeaderRewrite,omitempty"`
	UseEncryption     bool   `json:"useEncryption"`
	UseCompression    bool   `json:"useCompression"`
	SecretKey         string `json:"secretKey,omitempty"`

	HealthCheckType      string `json:"healthCheckType,omitempty"` // "" / tcp / http
	HealthCheckURL       string `json:"healthCheckURL,omitempty"`
	HealthCheckTimeoutS  int    `json:"healthCheckTimeoutS,omitempty"`
	HealthCheckMaxFailed int    `json:"healthCheckMaxFailed,omitempty"`
	HealthCheckIntervalS int    `json:"healthCheckIntervalS,omitempty"`

	Remark string `json:"remark,omitempty"`
}

// Config 是 frpc 的公共段配置 + 代理规则集合。
type Config struct {
	ServerAddr    string  `json:"serverAddr"`
	ServerPort    int     `json:"serverPort"`
	AuthToken     string  `json:"authToken"`
	AuthUser      string  `json:"authUser,omitempty"`
	Protocol      string  `json:"protocol"`
	TLSEnable     bool    `json:"tlsEnable"`
	TCPMux        bool    `json:"tcpMux"`
	PoolCount     int     `json:"poolCount"`
	ProxyURL      string  `json:"proxyURL,omitempty"`
	LogLevel      string  `json:"logLevel"`
	LogMaxDays    int     `json:"logMaxDays"`
	LoginFailExit bool    `json:"loginFailExit"`
	Proxies       []Proxy `json:"proxies"`
}

// Admin 是 frpc 内置管理接口的配置，由 manager 生成并独占使用，
// 不暴露给前端（用户不需要也不应该知道这个端口）。
type Admin struct {
	Addr     string
	Port     int
	User     string
	Password string
}

// Default 返回一份可直接打开的初始配置。
func Default() Config {
	return Config{
		ServerAddr:    "",
		ServerPort:    7000,
		Protocol:      "tcp",
		TLSEnable:     true,
		TCPMux:        true,
		PoolCount:     0,
		LogLevel:      "info",
		LogMaxDays:    3,
		LoginFailExit: false,
		Proxies:       []Proxy{},
	}
}

// Clone 深拷贝，避免调用方拿到切片后被并发修改。
func (c Config) Clone() Config {
	out := c
	out.Proxies = make([]Proxy, len(c.Proxies))
	copy(out.Proxies, c.Proxies)
	return out
}

// Validate 对用户输入做一次完整检查，返回第一条人类可读的错误。
func (c Config) Validate() error {
	if strings.TrimSpace(c.ServerAddr) == "" {
		return fmt.Errorf("请填写服务器地址（frps 的 IP 或域名）")
	}
	if c.ServerPort < 1 || c.ServerPort > 65535 {
		return fmt.Errorf("服务器端口必须在 1-65535 之间")
	}
	if !contains(Protocols, c.Protocol) {
		return fmt.Errorf("不支持的传输协议：%s", c.Protocol)
	}
	if !contains(LogLevels, c.LogLevel) {
		return fmt.Errorf("不支持的日志级别：%s", c.LogLevel)
	}
	if strings.Contains(c.AuthToken, "\n") {
		return fmt.Errorf("Token 不能包含换行符")
	}

	seen := make(map[string]bool, len(c.Proxies))
	for i, p := range c.Proxies {
		prefix := fmt.Sprintf("第 %d 条隧道（%s）：", i+1, p.Name)
		if p.Name == "" {
			return fmt.Errorf("第 %d 条隧道缺少名称", i+1)
		}
		if !nameRe.MatchString(p.Name) {
			return fmt.Errorf("%s名称只能包含字母、数字、下划线、点、短横线，且以字母或数字开头", prefix)
		}
		if seen[p.Name] {
			return fmt.Errorf("%s名称重复，frp 要求每条隧道名称唯一", prefix)
		}
		seen[p.Name] = true

		if !contains(ProxyTypes, p.Type) {
			return fmt.Errorf("%s不支持的隧道类型：%s", prefix, p.Type)
		}
		if p.LocalPort < 0 || p.LocalPort > 65535 {
			return fmt.Errorf("%s本地端口必须在 1-65535 之间", prefix)
		}
		if remotePortTypes[p.Type] {
			if p.RemotePort < 1 || p.RemotePort > 65535 {
				return fmt.Errorf("%s%s 类型必须填写远程端口", prefix, strings.ToUpper(p.Type))
			}
		}
		if p.LocalPort == 0 {
			return fmt.Errorf("%s必须填写本地端口", prefix)
		}
		if domainTypes[p.Type] {
			if strings.TrimSpace(p.CustomDomains) == "" && strings.TrimSpace(p.Subdomain) == "" {
				return fmt.Errorf("%s%s 类型必须填写自定义域名或子域名", prefix, strings.ToUpper(p.Type))
			}
		}
		if p2pTypes[p.Type] && strings.TrimSpace(p.SecretKey) == "" {
			return fmt.Errorf("%s%s 类型必须填写密钥（secretKey）", prefix, strings.ToUpper(p.Type))
		}
		if p.HealthCheckType != "" && p.HealthCheckType != "tcp" && p.HealthCheckType != "http" {
			return fmt.Errorf("%s健康检查类型只能是 tcp 或 http", prefix)
		}
		if p.HealthCheckType == "http" && strings.TrimSpace(p.HealthCheckURL) == "" {
			return fmt.Errorf("%s启用 HTTP 健康检查时必须填写检查 URL", prefix)
		}
	}
	return nil
}

// RequiresRestart 判断两次配置之间是否存在"热重载不支持"的公共段变化。
// frpc 的 reload 只能增删改代理，serverAddr/auth/transport/log 等公共段必须重启进程。
func RequiresRestart(a, b Config) bool {
	return a.ServerAddr != b.ServerAddr ||
		a.ServerPort != b.ServerPort ||
		a.AuthToken != b.AuthToken ||
		a.AuthUser != b.AuthUser ||
		a.Protocol != b.Protocol ||
		a.TLSEnable != b.TLSEnable ||
		a.TCPMux != b.TCPMux ||
		a.PoolCount != b.PoolCount ||
		a.ProxyURL != b.ProxyURL ||
		a.LogLevel != b.LogLevel ||
		a.LogMaxDays != b.LogMaxDays ||
		a.LoginFailExit != b.LoginFailExit
}

// ToTOML 把配置渲染成 frp v1 格式的 TOML 文本。
func (c Config) ToTOML(admin Admin, logPath string) string {
	var b strings.Builder

	b.WriteString("# 本文件由 frpc-qnap 自动生成，请通过 NAS 网页端修改。\n")
	b.WriteString("# 手工修改会在下次保存时被覆盖（原文件会备份为 frpc.toml.bak）。\n\n")

	line := func(k, v string) { b.WriteString(k + " = " + v + "\n") }
	q := func(s string) string { return quoteTOML(s) }
	num := func(n int) string { return strconv.Itoa(n) }
	boolean := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}

	line("serverAddr", q(c.ServerAddr))
	line("serverPort", num(c.ServerPort))
	if !c.LoginFailExit {
		line("loginFailExit", "false")
	}
	if c.AuthUser != "" {
		line("user", q(c.AuthUser))
	}

	b.WriteString("\n# 认证\n")
	line("auth.method", q("token"))
	line("auth.token", q(c.AuthToken))

	b.WriteString("\n# 传输\n")
	line("transport.protocol", q(c.Protocol))
	line("transport.tcpMux", boolean(c.TCPMux))
	if c.PoolCount > 0 {
		line("transport.poolCount", num(c.PoolCount))
	}
	line("transport.tls.enable", boolean(c.TLSEnable))
	if c.ProxyURL != "" {
		line("transport.proxyURL", q(c.ProxyURL))
	}

	b.WriteString("\n# 日志\n")
	line("log.to", q(logPath))
	line("log.level", q(c.LogLevel))
	line("log.maxDays", num(c.LogMaxDays))
	line("log.disablePrintColor", "true")

	b.WriteString("\n# 内置管理接口：仅供本机管理器调用，绑回环地址\n")
	line("webServer.addr", q(admin.Addr))
	line("webServer.port", num(admin.Port))
	line("webServer.user", q(admin.User))
	line("webServer.password", q(admin.Password))

	for _, p := range c.Proxies {
		b.WriteString("\n[[proxies]]\n")
		line("name", q(p.Name))
		line("type", q(p.Type))
		if p.LocalIP != "" {
			line("localIP", q(p.LocalIP))
		}
		if p.LocalPort > 0 {
			line("localPort", num(p.LocalPort))
		}
		if remotePortTypes[p.Type] {
			line("remotePort", num(p.RemotePort))
		}
		if p.CustomDomains != "" {
			line("customDomains", arrayTOML(p.CustomDomains))
		}
		if p.Subdomain != "" {
			line("subdomain", q(p.Subdomain))
		}
		if p.Locations != "" {
			line("locations", arrayTOML(p.Locations))
		}
		if p.HTTPUser != "" {
			line("httpUser", q(p.HTTPUser))
		}
		if p.HTTPPassword != "" {
			line("httpPassword", q(p.HTTPPassword))
		}
		if p.HostHeaderRewrite != "" {
			line("hostHeaderRewrite", q(p.HostHeaderRewrite))
		}
		if p.SecretKey != "" {
			line("secretKey", q(p.SecretKey))
		}
		if p.UseEncryption {
			line("transport.useEncryption", "true")
		}
		if p.UseCompression {
			line("transport.useCompression", "true")
		}
		if p.HealthCheckType != "" {
			line("healthCheck.type", q(p.HealthCheckType))
			if p.HealthCheckType == "http" && p.HealthCheckURL != "" {
				line("healthCheck.path", q(p.HealthCheckURL))
			}
			if p.HealthCheckTimeoutS > 0 {
				line("healthCheck.timeoutSeconds", num(p.HealthCheckTimeoutS))
			}
			if p.HealthCheckMaxFailed > 0 {
				line("healthCheck.maxFailed", num(p.HealthCheckMaxFailed))
			}
			if p.HealthCheckIntervalS > 0 {
				line("healthCheck.intervalSeconds", num(p.HealthCheckIntervalS))
			}
		}
	}
	return b.String()
}

// Store 负责 manager.json 的并发安全读写。
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// NewStore 打开（或初始化）指定路径的配置文件。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return s, nil
	}
	// 以默认值为基底再覆盖，这样旧版本配置缺字段（或首次安装的空模板 {}）
	// 也能得到合理的默认值，而不是把 TLS/TCPMux 变成 false。
	c := Default()
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", filepath.Base(path), err)
	}
	if c.Proxies == nil {
		c.Proxies = []Proxy{}
	}
	s.cfg = c
	return s, nil
}

// Get 返回当前配置的副本。
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

// Save 校验并持久化配置。写入采用临时文件 + rename，避免掉电写坏。
func (s *Store) Save(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	s.cfg = c.Clone()
	return nil
}

// writeFileAtomic 原子替换写入，权限 0600（配置里有 token）。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// WriteFileAtomic 供其它包复用（如写入 frpc.toml）。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomic(path, data, perm)
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// quoteTOML 生成 TOML 基本字符串。frp 配置里出现的都是普通文本，
// 只需处理反斜杠、双引号与控制字符。
func quoteTOML(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04X`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// arrayTOML 把逗号分隔的字符串转成 TOML 字符串数组。
func arrayTOML(s string) string {
	parts := strings.Split(s, ",")
	items := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			items = append(items, quoteTOML(p))
		}
	}
	return "[" + strings.Join(items, ", ") + "]"
}
