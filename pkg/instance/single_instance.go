package instance

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/utils"
)

const (
	pingMsg = "PING_BBDOWN_PRO\n"
	pongMsg = "PONG_BBDOWN_PRO"
	showMsg = "SHOW_BBDOWN_PRO\n"
)

// InstanceLock 定义单实例系统排他锁接口
type InstanceLock interface {
	Release() error
}

var currentLock InstanceLock

// SetupSingleInstance 确保单例运行并支持双向握手校验与唤醒
func SetupSingleInstance(onShow func()) error {
	configDir := config.GetConfigDir()
	lockFile := filepath.Join(configDir, "instance.lock")

	// 1. 获取底层系统级排他锁 (Windows: Named Mutex / macOS & Linux: flock)
	sysLock, err := tryAcquireSystemLock(configDir)
	if errors.Is(err, errAlreadyRunning) {
		// 当前是从实例，尝试唤醒已存在的主实例并优雅退出
		wakeExistingInstance(lockFile)
		os.Exit(0)
		return nil
	}
	if err != nil {
		// 遇到非冲突性的文件系统未知异常，记录并降级继续
		fmt.Printf("获取系统单例锁出现异常: %v，降级继续启动\n", err)
	}

	currentLock = sysLock

	// 2. 当前进程作为合法主实例启动监听
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if currentLock != nil {
			_ = currentLock.Release()
		}
		return err
	}

	port := listener.Addr().(*net.TCPAddr).Port
	_ = utils.AtomicWriteFile(lockFile, []byte(fmt.Sprintf("%d", port)), 0644)

	go func() {
		defer func() {
			_ = listener.Close()
			_ = os.Remove(lockFile)
			if currentLock != nil {
				_ = currentLock.Release()
			}
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
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

func wakeExistingInstance(lockFile string) {
	// 重试最多 5 次读取端口并发送唤醒指令 (覆盖主实例刚启动写入文件的毫秒级微小延迟)
	for attempt := 0; attempt < 5; attempt++ {
		data, err := os.ReadFile(lockFile)
		if err == nil {
			portStr := strings.TrimSpace(string(data))
			if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
				if err == nil {
					_ = conn.SetDeadline(time.Now().Add(800 * time.Millisecond))
					_, _ = conn.Write([]byte(pingMsg))
					reader := bufio.NewReader(conn)
					reply, rErr := reader.ReadString('\n')
					if rErr == nil && strings.TrimSpace(reply) == pongMsg {
						_, _ = conn.Write([]byte(showMsg))
						_ = conn.Close()
						fmt.Println("检测到已有 BBDown 实例运行中，已唤醒该实例。")
						return
					}
					_ = conn.Close()
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	fmt.Println("检测到已有 BBDown 实例运行中，已退出当前进程。")
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
