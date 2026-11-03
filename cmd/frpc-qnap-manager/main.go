// frpc-qnap-manager 是 frpc 在 QNAP 上的管理进程：
// 提供网页端（经 QTS 反向代理）、生成 frpc 配置、守护 frpc 子进程。
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zeisscai/frpc-qnap/internal/server"
)

// version 由构建脚本通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	var (
		dataDir = flag.String("data", "", "数据目录（默认自动探测 QNAP 安装路径）")
		frpcBin = flag.String("frpc", "", "frpc 可执行文件路径（默认 <data>/frpc）")
		listen  = flag.String("listen", "127.0.0.1:7911", "监听地址，只应绑定回环地址")
		dev     = flag.Bool("dev", false, "开发模式：前端资源从磁盘读取")
		adminP  = flag.Int("admin-port", 0, "frpc 管理接口端口（默认 7400，占用时自动顺延）")
		showVer = flag.Bool("version", false, "打印版本号后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	dir := *dataDir
	if dir == "" {
		dir = detectDataDir()
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}

	bin := *frpcBin
	if bin == "" {
		bin = filepath.Join(dir, "frpc")
	}

	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		log.Fatalf("创建日志目录失败：%v", err)
	}
	if f, err := os.OpenFile(filepath.Join(dir, "logs", "manager.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[manager] ")

	srv, err := server.New(server.Options{
		DataDir:    dir,
		FrpcBin:    bin,
		Dev:        *dev,
		Version:    version,
		ListenAddr: *listen,
		AdminPort:  *adminP,
	})
	if err != nil {
		log.Fatalf("初始化失败：%v", err)
	}
	log.Printf("frpc-qnap manager %s 启动，数据目录 %s，frpc %s", version, dir, bin)

	if err := srv.Bootstrap(); err != nil {
		// 启动失败不退出：用户仍可通过网页端修正配置。
		log.Printf("frpc 启动失败：%v", err)
	}

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("HTTP 监听 %s（经 QTS 代理访问时不需此端口）", *listen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常：%v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("收到退出信号，正在停止 frpc…")

	done := make(chan struct{})
	go func() {
		_ = httpSrv.Close()
		srv.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		log.Printf("停止超时，强制退出")
	}
	log.Printf("已退出")
}

// detectDataDir 优先使用 QNAP 的标准安装目录；开发机上退化为 ./devdata。
func detectDataDir() string {
	const qnapBase = "/share/CACHEDEV1_DATA/.qpkg/frpc-qnap"
	if st, err := os.Stat("/share/CACHEDEV1_DATA/.qpkg"); err == nil && st.IsDir() {
		return qnapBase
	}
	if st, err := os.Stat("/share/ZFS1_DATA/.qpkg"); err == nil && st.IsDir() {
		return "/share/ZFS1_DATA/.qpkg/frpc-qnap"
	}
	return "devdata"
}
