//go:build windows

package bilibili

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"golang.org/x/sys/windows/registry"
)

// extractExePath 从包含参数或引号的命令行中提取纯净的 .exe 文件路径
func extractExePath(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if strings.HasPrefix(cmd, "\"") {
		if idx := strings.Index(cmd[1:], "\""); idx != -1 {
			return cmd[1 : idx+1]
		}
	}
	if idx := strings.Index(cmd, " "); idx != -1 {
		return cmd[:idx]
	}
	return cmd
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// isChromiumBrowser 校验可执行文件是否为 Chromium 内核浏览器
func isChromiumBrowser(exePath string) bool {
	lower := strings.ToLower(filepath.Base(exePath))
	keywords := []string{"msedge", "chrome", "brave", "360", "qqbrowser", "vivaldi", "opera", "chromium", "centbrowser"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// findBrowserFromRegistry 从 Windows 注册表探测默认浏览器或 App Paths
func findBrowserFromRegistry() string {
	// 1. 探测系统当前默认 HTTP 协议关联浏览器
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\Shell\Associations\UrlAssociations\http\UserChoice`, registry.QUERY_VALUE)
	if err == nil {
		progID, _, err := k.GetStringValue("ProgId")
		k.Close()
		if err == nil && progID != "" {
			cmdKey, err := registry.OpenKey(registry.CLASSES_ROOT, progID+`\shell\open\command`, registry.QUERY_VALUE)
			if err == nil {
				cmdVal, _, err := cmdKey.GetStringValue("")
				cmdKey.Close()
				if err == nil && cmdVal != "" {
					exe := extractExePath(cmdVal)
					if isChromiumBrowser(exe) && fileExists(exe) {
						return exe
					}
				}
			}
		}
	}

	// 2. 探测注册表 App Paths 中的常见 Chromium 浏览器
	appPaths := []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\brave.exe`,
	}
	for _, p := range appPaths {
		for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
			k, err := registry.OpenKey(root, p, registry.QUERY_VALUE)
			if err == nil {
				val, _, err := k.GetStringValue("")
				k.Close()
				if err == nil && val != "" {
					exe := extractExePath(val)
					if fileExists(exe) {
						return exe
					}
				}
			}
		}
	}

	return ""
}

// findWindowsChromiumBrowser 智能多阶检测 Windows 上的 Edge / Chrome / Chromium 浏览器
func findWindowsChromiumBrowser() (string, error) {
	// 1. 优先使用注册表中找到的默认/已注册 Chromium 浏览器
	if regPath := findBrowserFromRegistry(); regPath != "" && fileExists(regPath) {
		return regPath, nil
	}

	progFiles := os.Getenv("ProgramFiles")
	progFiles86 := os.Getenv("ProgramFiles(x86)")
	localAppData := os.Getenv("LocalAppData")
	userProfile := os.Getenv("USERPROFILE")

	// 2. 依次检测标准预设安装路径（Edge 是 Win10/Win11 100% 标配自带）
	candidates := []string{
		// Microsoft Edge (Windows 10/11 原生预装)
		filepath.Join(progFiles86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(progFiles, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(localAppData, `Microsoft\Edge\Application\msedge.exe`),
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,

		// Google Chrome
		filepath.Join(progFiles, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(progFiles86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(localAppData, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(userProfile, `AppData\Local\Google\Chrome\Application\chrome.exe`),
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,

		// Brave Browser
		filepath.Join(progFiles, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		filepath.Join(progFiles86, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		filepath.Join(localAppData, `BraveSoftware\Brave-Browser\Application\brave.exe`),

		// 360 极速/安全浏览器
		filepath.Join(localAppData, `360Chrome\Chrome\Application\360chrome.exe`),
		filepath.Join(progFiles86, `360\360se6\Application\360se.exe`),
		filepath.Join(progFiles, `360\360se6\Application\360se.exe`),

		// QQ 浏览器
		filepath.Join(progFiles86, `Tencent\QQBrowser\QQBrowser.exe`),
		filepath.Join(progFiles, `Tencent\QQBrowser\QQBrowser.exe`),

		// Vivaldi / Chromium
		filepath.Join(localAppData, `Vivaldi\Application\vivaldi.exe`),
		filepath.Join(localAppData, `Chromium\Application\chrome.exe`),
	}

	for _, path := range candidates {
		if path != "" && fileExists(path) {
			return path, nil
		}
	}

	// 3. 尝试从系统 %PATH% 环境变量中搜寻
	names := []string{"msedge.exe", "msedge", "chrome.exe", "chrome", "brave.exe", "brave"}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path, nil
		}
	}

	return "", fmt.Errorf("未在系统中检测到 Microsoft Edge、Google Chrome 或其他 Chromium 内核浏览器，请先安装 Edge 或 Chrome")
}

// OpenNativeBrowserLogin opens a native browser window for Bilibili login.
// On Windows, it uses chromedp to launch the user's Edge/Chrome in app mode.
func (c *Client) OpenNativeBrowserLogin(appCtx context.Context) error {
	// 1. 探测可用的 Edge / Chrome / Chromium 浏览器执行路径
	browserPath, err := findWindowsChromiumBrowser()
	if err != nil {
		return err
	}

	// 2. Create an ephemeral/temporary user data directory for a clean session
	tmpDir, err := os.MkdirTemp("", "bili_login_*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer func() {
		go func(dir string) {
			for i := 0; i < 5; i++ {
				time.Sleep(500 * time.Millisecond)
				if err := os.RemoveAll(dir); err == nil {
					return
				}
			}
		}(tmpDir)
	}()

	// 3. Configure Chrome/Edge launch options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("app", "data:text/html,<html><head><title>哔哩哔哩 - 登录</title></head><body style=\"background:#fff;display:flex;justify-content:center;align-items:center;height:100vh;font-family:sans-serif;\">正在加载安全环境...</body></html>"),
		chromedp.Flag("window-size", "860,600"),
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-sync", true),
		chromedp.UserDataDir(tmpDir),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(appCtx, opts...)
	defer cancelAlloc()

	// Ensure the browser window itself is closed if we exit early
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	defer cancelCtx()

	// 4. Define the auto-click injection script
	clickScript := `
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
			
			let popoverBtn = document.querySelector('.login-panel-popover .login-btn') || 
							 document.querySelector('.bili-header__login-entry .login-btn') || 
							 document.querySelector('.header-login-panel .btn-login');
			if (popoverBtn) {
				triggerClick(popoverBtn);
				return;
			}
			
			let xpath = "//div[contains(text(), '立即登录')] | //button[contains(text(), '立即登录')] | //span[contains(text(), '立即登录')]";
			let btn = document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
			if (btn) {
				triggerClick(btn);
				return;
			}
			
			let topBtn = document.querySelector('.header-login-entry') || document.querySelector('.right-entry-item.go-login-btn');
			if (topBtn) {
				triggerClick(topBtn);
			}
		}, 30);
		
		setTimeout(() => clearInterval(pollTimer), 10000);
	})();
	`

	// 5. Inject script at Document Start and Navigate to Bilibili
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(clickScript).Do(c)
			return err
		}),
		chromedp.Navigate("https://www.bilibili.com/"),
	)
	if err != nil {
		return fmt.Errorf("failed to launch browser: %v", err)
	}

	// 6. High-frequency cookie polling
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	// Set a maximum timeout for the login session (e.g. 5 minutes)
	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-timeout:
			return fmt.Errorf("login timeout")
		case <-ticker.C:
			var cookies []*network.Cookie
			err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
				var err error
				cookies, err = network.GetCookies().Do(c)
				return err
			}))

			if err != nil {
				// Window probably closed manually by user or chromedp context cancelled
				return fmt.Errorf("browser closed by user")
			}

			var sessData, biliJct, dedeUid, buvid3 string
			for _, cookie := range cookies {
				// Only match cookies for bilibili.com
				if cookie.Domain != "" {
					if cookie.Name == "SESSDATA" {
						sessData = cookie.Value
					}
					if cookie.Name == "bili_jct" {
						biliJct = cookie.Value
					}
					if cookie.Name == "DedeUserID" {
						dedeUid = cookie.Value
					}
					if cookie.Name == "buvid3" {
						buvid3 = cookie.Value
					}
				}
			}

			// If SESSDATA is captured, login is complete
			if sessData != "" {
				fmt.Println("BILI_NATIVE_LOGIN_SUCCESS (Windows chromedp)")
				rawCookieStr := fmt.Sprintf("SESSDATA=%s; bili_jct=%s; DedeUserID=%s; buvid3=%s", sessData, biliJct, dedeUid, buvid3)
				c.ParseAndSaveRawCookie(rawCookieStr)
				_, _ = c.GetUserInfo(context.Background())
				// Return gracefully, which triggers defer cancelCtx() closing the browser window instantly
				return nil
			}
		}
	}
}
