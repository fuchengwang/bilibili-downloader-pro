package instance

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bilibili_downloader/pkg/config"
)

const (
	pingMsg = "PING_BBDOWN_PRO\n"
	pongMsg = "PONG_BBDOWN_PRO"
	showMsg = "SHOW_BBDOWN_PRO\n"
)

// SetupSingleInstance 确保单例运行并支持双向握手校验与唤醒
func SetupSingleInstance(onShow func()) error {
	lockFile := filepath.Join(config.GetConfigDir(), "instance.lock")

	// 1. 尝试读取已有端口并进行双向握手验证
	if data, err := os.ReadFile(lockFile); err == nil {
		portStr := strings.TrimSpace(string(data))
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
			// 设置严格的超时限制，防止连接挂起
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 400*time.Millisecond)
			if err == nil {
				_ = conn.SetDeadline(time.Now().Add(600 * time.Millisecond))
				// 发送探针
				_, _ = conn.Write([]byte(pingMsg))
				reader := bufio.NewReader(conn)
				reply, rErr := reader.ReadString('\n')
				if rErr == nil && strings.TrimSpace(reply) == pongMsg {
					// 确认为真实 BBDown 运行实例，通知其显示窗口
					_, _ = conn.Write([]byte(showMsg))
					_ = conn.Close()
					fmt.Println("检测到已有 BBDown 实例运行中，已唤醒该实例并退出当前进程。")
					os.Exit(0)
				}
				_ = conn.Close()
				// 如果对端返回不匹配或超时，说明端口已被其他本地进程占用，绝不盲目闪退，继续作为主实例启动
			}
		}
	}

	// 2. 当前进程作为主实例启动监听
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	port := listener.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(lockFile, []byte(fmt.Sprintf("%d", port)), 0644)

	go func() {
		defer func() {
			_ = listener.Close()
			_ = os.Remove(lockFile)
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				// 如果 listener 被关闭，退出监听
				if strings.Contains(err.Error(), "use of closed network connection") {
					return
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}

			go handleSingleInstanceConn(conn, onShow)
		}
	}()

	return nil
}

func handleSingleInstanceConn(conn net.Conn, onShow func()) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		switch cmd {
		case "PING_BBDOWN_PRO":
			_, _ = conn.Write([]byte(pongMsg + "\n"))
		case "SHOW_BBDOWN_PRO", "show":
			if onShow != nil {
				onShow()
			}
			return
		}
	}
}
