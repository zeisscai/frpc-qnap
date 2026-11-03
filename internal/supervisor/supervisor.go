// Package supervisor 负责 frpc 子进程的生命周期：启动、优雅停止、崩溃后退避重启。
//
// manager 只守护一个 frpc 实例。QTS 的启停脚本只操作 manager，
// frpc 的存活由这里保证，这样"进程挂了自动拉起"不需要依赖 QTS。
package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	// maxRestarts 是连续崩溃的最大重启次数，超过后进入 failed 状态，等待人工介入，
	// 避免配置写错时无限重启刷日志。
	maxRestarts = 10
	backoffMin  = time.Second
	backoffMax  = 30 * time.Second
)

// State 是暴露给 UI 的进程状态快照。
type State struct {
	Running   bool   `json:"running"`
	Pid       int    `json:"pid"`
	StartedAt string `json:"startedAt,omitempty"`
	Restarts  int    `json:"restarts"`
	Failed    bool   `json:"failed"`
	LastError string `json:"lastError,omitempty"`
}

// Supervisor 守护单个 frpc 进程。
type Supervisor struct {
	bin     string
	cfgPath string
	logPath string

	mu            sync.Mutex
	cmd           *exec.Cmd
	running       bool
	stopRequested bool
	stopCh        chan struct{}
	doneCh        chan struct{}
	startedAt     time.Time
	restarts      int
	failed        bool
	lastErr       string
}

// New 创建守护器。bin 为 frpc 可执行文件路径，logPath 为 frpc 的日志文件。
func New(bin, cfgPath, logPath string) *Supervisor {
	return &Supervisor{bin: bin, cfgPath: cfgPath, logPath: logPath}
}

// Start 启动 frpc 并进入守护循环。已在运行则直接返回。
func (s *Supervisor) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	if _, err := os.Stat(s.bin); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("找不到 frpc 可执行文件：%s", s.bin)
	}
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.running = true
	s.stopRequested = false
	s.failed = false
	s.restarts = 0
	s.lastErr = ""
	s.mu.Unlock()

	go s.loop()
	return nil
}

// Stop 先发 SIGTERM，超时后 SIGKILL。timeout 建议 15s（QTS 关机等待上限 30s）。
func (s *Supervisor) Stop(timeout time.Duration) {
	s.mu.Lock()
	if !s.running || s.stopRequested {
		s.mu.Unlock()
		return
	}
	s.stopRequested = true
	close(s.stopCh)
	cmd := s.cmd
	done := s.doneCh
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-done:
	case <-time.After(timeout):
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}

	s.mu.Lock()
	s.running = false
	s.cmd = nil
	s.mu.Unlock()
}

// Restart 停止后重新启动。
func (s *Supervisor) Restart(timeout time.Duration) error {
	s.Stop(timeout)
	return s.Start()
}

// State 返回当前状态快照。
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{
		Running:   s.running && s.cmd != nil,
		Restarts:  s.restarts,
		Failed:    s.failed,
		LastError: s.lastErr,
	}
	if s.cmd != nil && s.cmd.Process != nil {
		st.Pid = s.cmd.Process.Pid
	}
	if !s.startedAt.IsZero() && st.Running {
		st.StartedAt = s.startedAt.Format(time.RFC3339)
	}
	return st
}

func (s *Supervisor) loop() {
	defer close(s.doneCh)

	backoff := backoffMin
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		cmd := exec.Command(s.bin, "-c", s.cfgPath)
		var logFile *os.File
		if f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			logFile = f
			cmd.Stdout = f
			cmd.Stderr = f
		}

		s.mu.Lock()
		s.cmd = cmd
		s.startedAt = time.Now()
		s.mu.Unlock()

		err := cmd.Run()
		if logFile != nil {
			_ = logFile.Close()
		}

		s.mu.Lock()
		s.cmd = nil
		s.mu.Unlock()

		select {
		case <-s.stopCh:
			return
		default:
		}

		s.mu.Lock()
		if err != nil {
			s.lastErr = fmt.Sprintf("frpc 异常退出：%v", err)
		} else {
			s.lastErr = "frpc 进程正常退出（退出码 0），但未收到停止指令"
		}
		s.restarts++
		exceeded := s.restarts > maxRestarts
		if exceeded {
			s.failed = true
			s.running = false
		}
		s.mu.Unlock()

		if exceeded {
			return
		}

		timer := time.NewTimer(backoff)
		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < backoffMax {
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
		}
	}
}
