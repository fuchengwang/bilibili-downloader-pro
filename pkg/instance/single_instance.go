package instance

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bilibili_downloader/pkg/config"
)

// SetupSingleInstance checks if another instance is running.
// If it is, it sends a "show" signal to it and exits the current process.
// If not, it starts a local listener and calls onShow when a signal is received.
func SetupSingleInstance(onShow func()) error {
	lockFile := filepath.Join(config.GetConfigDir(), "instance.lock")

	// Try to read existing port
	if data, err := os.ReadFile(lockFile); err == nil {
		portStr := strings.TrimSpace(string(data))
		if port, err := strconv.Atoi(portStr); err == nil {
			// Try to connect
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				// Connected to existing instance, send signal
				_, _ = conn.Write([]byte("show\n"))
				conn.Close()
				fmt.Println("Another instance is already running. Signaling it to show and exiting.")
				os.Exit(0)
			}
		}
	}

	// We are the primary instance
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	port := listener.Addr().(*net.TCPAddr).Port
	err = os.WriteFile(lockFile, []byte(fmt.Sprintf("%d", port)), 0644)
	if err != nil {
		return err
	}

	go func() {
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				continue
			}
			reader := bufio.NewReader(conn)
			msg, _ := reader.ReadString('\n')
			if strings.TrimSpace(msg) == "show" {
				if onShow != nil {
					onShow()
				}
			}
			conn.Close()
		}
	}()

	return nil
}
