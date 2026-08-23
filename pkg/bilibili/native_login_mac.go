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

// OpenNativeBrowserLogin opens a native macOS WebKit window and blocks until SESSDATA is acquired or window is closed.
func (c *Client) OpenNativeBrowserLogin(ctx context.Context) error {
	tmpFile := filepath.Join(os.TempDir(), "bilibili_mac_login.swift")
	err := os.WriteFile(tmpFile, []byte(swiftLoginScript), 0600)
	if err != nil {
		return fmt.Errorf("failed to create swift script: %w", err)
	}
	defer os.Remove(tmpFile)

	cmd := exec.CommandContext(ctx, "swift", tmpFile)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		// user closed window or process killed
		return fmt.Errorf("登录已取消或失败: %v, stderr: %s", err, stderr.String())
	}

	output := out.String()
	if strings.Contains(output, "BILI_NATIVE_LOGIN_SUCCESS:") {
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "SESSDATA=") {
				c.ParseAndSaveRawCookie(line)
				return nil
			}
		}
	}

	return fmt.Errorf("未获取到登录 Cookie")
}
