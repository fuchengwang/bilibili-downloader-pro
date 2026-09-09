//go:build darwin

package bilibili

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// swiftLoginScript is the Swift code that opens a native macOS WKWebView login window.
const swiftLoginScript = `
import Cocoa
import WebKit

class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate {
    var window: NSWindow!
    var webView: WKWebView!
    var timer: Timer?

    func applicationDidFinishLaunching(_ aNotification: Notification) {
        let app = NSApplication.shared
        app.setActivationPolicy(.regular)
        
        let rect = NSRect(x: 0, y: 0, width: 860, height: 600)
        window = NSWindow(contentRect: rect,
                          styleMask: [.titled, .closable, .resizable, .miniaturizable],
                          backing: .buffered,
                          defer: false)
        window.center()
        window.title = "哔哩哔哩 - 登录"
        window.makeKeyAndOrderFront(nil)
        
        // Ensure clean session & standard modern UA
        let config = WKWebViewConfiguration()
        config.websiteDataStore = WKWebsiteDataStore.nonPersistent()
        config.applicationNameForUserAgent = "Version/18.0 Safari/605.1.15 Chrome/133.0.0.0"
        
        // 核心优化：在 HTML 文档刚开始解析时即刻注入脚本，无需等待页面庞大的资源（视频封面、广告等）完全加载
        let clickScript = """
        (function() {
            let pollTimer = setInterval(() => {
                if (document.querySelector('.bili-mini-login-wrapper') || document.querySelector('.bili-mini-mask') || document.querySelector('.login-scan-box')) {
                    clearInterval(pollTimer);
                    return;
                }
                
                function triggerClick(el) {
                    if (!el) return false;
                    ['mousedown', 'mouseup', 'click'].forEach(t => {
                        el.dispatchEvent(new MouseEvent(t, { bubbles: true, cancelable: true, view: window }));
                    });
                    return true;
                }
                
                // 1. 优先尝试悬浮框里的“立即登录”按钮
                let popoverBtn = document.querySelector('.login-panel-popover .login-btn') || 
                                 document.querySelector('.bili-header__login-entry .login-btn') || 
                                 document.querySelector('.header-login-panel .btn-login');
                if (popoverBtn) {
                    triggerClick(popoverBtn);
                    return;
                }
                
                // 2. 文本匹配立即登录
                let xpath = "//div[contains(text(), '立即登录')] | //button[contains(text(), '立即登录')] | //span[contains(text(), '立即登录')]";
                let btn = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
                if (btn) {
                    triggerClick(btn);
                    return;
                }
                
                // 3. 点击顶部导航栏登录按钮
                let topBtn = document.querySelector('.header-login-entry') || document.querySelector('.right-entry-item.go-login-btn');
                if (topBtn) {
                    triggerClick(topBtn);
                }
            }, 30);
            
            setTimeout(() => clearInterval(pollTimer), 10000);
        })();
        """
        
        let userScript = WKUserScript(source: clickScript, injectionTime: .atDocumentStart, forMainFrameOnly: true)
        config.userContentController.addUserScript(userScript)
        
        webView = WKWebView(frame: rect, configuration: config)
        webView.navigationDelegate = self
        window.contentView = webView
        
        let url = URL(string: "https://www.bilibili.com/")!
        webView.load(URLRequest(url: url))
        
        app.activate(ignoringOtherApps: true)
        
        // Poll for cookie
        timer = Timer.scheduledTimer(withTimeInterval: 0.8, repeats: true) { t in
            self.webView.configuration.websiteDataStore.httpCookieStore.getAllCookies { cookies in
                var sessData = ""
                var biliJct = ""
                var dedeUid = ""
                var buvid3 = ""
                var rawCookies = [String]()
                
                for cookie in cookies {
                    if cookie.domain.contains("bilibili.com") {
                        rawCookies.append("\(cookie.name)=\(cookie.value)")
                        if cookie.name == "SESSDATA" { sessData = cookie.value }
                        if cookie.name == "bili_jct" { biliJct = cookie.value }
                        if cookie.name == "DedeUserID" { dedeUid = cookie.value }
                        if cookie.name == "buvid3" { buvid3 = cookie.value }
                    }
                }
                
                if !sessData.isEmpty {
                    print("BILI_NATIVE_LOGIN_SUCCESS:")
                    print("SESSDATA=\(sessData); bili_jct=\(biliJct); DedeUserID=\(dedeUid); buvid3=\(buvid3)")
                    t.invalidate()
                    NSApplication.shared.terminate(self)
                }
            }
        }
    }
    
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        return true
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
`

// OpenNativeBrowserLogin 优先使用应用内置预编译登录器，环境无开发工具时自动平滑降级引导
func (c *Client) OpenNativeBrowserLogin(ctx context.Context) error {
	// 1. 优先检测是否存在预编译好的原生二进制 Helper
	execDir := filepath.Dir(os.Args[0])
	candidates := []string{
		filepath.Join(execDir, "bili-mac-login"),
		filepath.Join(execDir, "..", "MacOS", "bili-mac-login"),
		filepath.Join(execDir, "..", "Resources", "bili-mac-login"),
		filepath.Join(os.TempDir(), "bili_mac_login_bin"),
	}

	var helperBin string
	for _, cand := range candidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			helperBin = cand
			break
		}
	}

	if helperBin != "" {
		return c.runNativeLoginCmd(ctx, exec.CommandContext(ctx, helperBin))
	}

	// 2. 检测系统是否真正安装了 Xcode / Command Line Tools (使用 xcode-select -p 避免命中 /usr/bin/swift 导致系统弹窗)
	if hasDevTools() {
		if swiftPath, err := exec.LookPath("swift"); err == nil && swiftPath != "" {
			tmpFile := filepath.Join(os.TempDir(), "bilibili_mac_login.swift")
			if wErr := os.WriteFile(tmpFile, []byte(swiftLoginScript), 0600); wErr == nil {
				defer os.Remove(tmpFile)
				return c.runNativeLoginCmd(ctx, exec.CommandContext(ctx, swiftPath, tmpFile))
			}
		}
	}

	// 3. 干净系统兜底：打开系统默认浏览器，并引导用户使用扫码或粘贴 Cookie
	_ = exec.CommandContext(ctx, "open", "https://passport.bilibili.com/login").Start()
	return fmt.Errorf("已为您在默认浏览器中打开登录页面，登录后可在「填入 Cookie」中粘贴凭证，或推荐直接使用更便捷的官方「扫码登录」")
}

func hasDevTools() bool {
	cmd := exec.Command("xcode-select", "-p")
	return cmd.Run() == nil
}

func (c *Client) runNativeLoginCmd(ctx context.Context, cmd *exec.Cmd) error {
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("登录窗口已关闭或已取消: %v", err)
	}

	output := out.String()
	if strings.Contains(output, "BILI_NATIVE_LOGIN_SUCCESS:") {
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "SESSDATA=") {
				_ = c.ParseAndSaveRawCookie(line)
				_, _ = c.GetUserInfo(context.Background())
				return nil
			}
		}
	}

	return fmt.Errorf("未获取到有效登录 Cookie")
}
