package instance

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/config"
)

func TestSingleInstanceHandshake(t *testing.T) {
	// 使用隔离的临时目录测试
	tmpDir := t.TempDir()
	lockFile := filepath.Join(tmpDir, "instance.lock")

	// 模拟已存在的主实例监听
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	_ = os.WriteFile(lockFile, []byte(strconv.Itoa(port)), 0644)

	var showTriggered int32
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(line) == "PING_BBDOWN_PRO" {
			_, _ = conn.Write([]byte(pongMsg + "\n"))
		}
		cmd, _ := reader.ReadString('\n')
		if strings.TrimSpace(cmd) == "SHOW_BBDOWN_PRO" {
			atomic.StoreInt32(&showTriggered, 1)
		}
	}()

	// 模拟第二个实例连接并握手
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("连接主实例失败: %v", err)
	}
	defer conn.Close()

	_, _ = conn.Write([]byte(pingMsg))
	reader := bufio.NewReader(conn)
	reply, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(reply) != pongMsg {
		t.Fatalf("握手响应异常: reply=%s, err=%v", reply, err)
	}

	_, _ = conn.Write([]byte(showMsg))
	time.Sleep(100 * time.Millisecond)

	if atomic.LoadInt32(&showTriggered) != 1 {
		t.Fatal("未成功触发主实例 onShow 回调")
	}
}

func TestSingleInstanceIgnoreForeignPort(t *testing.T) {
	// 测试如果端口被其它无关服务占用，绝不误退
	tmpDir := t.TempDir()
	cfg := config.NewConfigManager(tmpDir)
	_ = cfg

	foreignListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer foreignListener.Close()

	port := foreignListener.Addr().(*net.TCPAddr).Port
	go func() {
		conn, err := foreignListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// 模拟第三方服务（如 Redis / HTTP），返回不是 PONG_BBDOWN_PRO 的内容
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	}()

	// 验证探针连接第三方服务时判定握手失败
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, _ = conn.Write([]byte(pingMsg))
	reader := bufio.NewReader(conn)
	reply, _ := reader.ReadString('\n')

	if strings.TrimSpace(reply) == pongMsg {
		t.Fatal("第三方服务响应不应匹配 PONG_BBDOWN_PRO")
	}
}
