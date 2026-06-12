//go:build !darwin

package main

import "log"

func runNativeWindow(url string) {
	log.Fatal("窗口模式（-window）目前仅支持 macOS，请直接运行服务并用浏览器访问 " + url)
}
