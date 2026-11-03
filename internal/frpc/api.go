// Package frpc 封装 frpc 内置管理 HTTP API（/api/status、/api/reload 等）。
//
// frpc 只有在配置了 webServer 段时才会开启该接口；本项目始终把它绑定在
// 127.0.0.1 并加 Basic Auth，仅由本管理器调用，不对用户暴露端口。
package frpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ProxyStatus 对应 frpc /api/status 返回的单条代理状态。
type ProxyStatus struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Err        string `json:"err"`
	LocalAddr  string `json:"local_addr"`
	Plugin     string `json:"plugin"`
	RemoteAddr string `json:"remote_addr"`
}

// Status 是 /api/status 的完整响应：按代理类型分组。
type Status map[string][]ProxyStatus

// Client 是 frpc 管理接口的最小客户端。
type Client struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client
}

// New 创建客户端。addr 形如 127.0.0.1:7400。
func New(addr, user, pass string) *Client {
	return &Client{
		baseURL: "http://" + addr,
		user:    user,
		pass:    pass,
		http:    &http.Client{Timeout: 3 * time.Second},
	}
}

// Status 拉取所有代理的运行状态。
func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	if err := c.do(ctx, http.MethodGet, "/api/status", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Reload 触发热重载。仅代理规则变化可用，公共段变化需重启进程。
//
// 注意：frpc 的 /api/reload 成功时返回 HTTP 200 且响应体为空，
// 因此这里不能按 JSON 解析，否则会误判为失败并触发不必要的重启。
func (c *Client) Reload(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/reload", nil)
}

// Stop 请求 frpc 自行退出（进程守护会感知到退出）。
func (c *Client) Stop(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/stop", nil)
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 frpc 管理接口：%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("frpc 管理接口认证失败（口令不匹配）")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("frpc 管理接口返回 HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 frpc 响应失败：%w", err)
	}
	return nil
}
