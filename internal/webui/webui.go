// Package webui 内嵌前端静态资源。
//
// 前端不使用构建工具（原生 ES 模块），这样打包时不需要 Node 运行时，
// 也便于直接改文件调试：开发模式下 manager 会优先读磁盘目录。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed assets
var embedded embed.FS

// FS 返回前端资源根目录。
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
