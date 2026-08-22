//go:build windows

package bilibili

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// OpenNativeBrowserLogin opens a native browser window for Bilibili login.
// On Windows, it uses chromedp to launch the user's Edge/Chrome in app mode.
func (c *Client) OpenNativeBrowserLogin(appCtx context.Context) error {
	// 1. Create an ephemeral/temporary user data directory for a clean session
	tmpDir, err := os.MkdirTemp("", "bili_login_*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 2. Configure Chrome/Edge launch options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("app", "data:text/html,<html><head><title>哔哩哔哩 - 登录</title></head><body style=\"background:#fff;display:flex;justify-content:center;align-items:center;height:100vh;font-family:sans-serif;\">正在加载安全环境...</body></html>"),
		chromedp.Flag("window-size", "860,600"),
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-sync", true),
		chromedp.UserDataDir(tmpDir),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()

	// Ensure the browser window itself is closed if we exit early
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	defer cancelCtx()

	// 3. Define the auto-click injection script
	// Identical to Mac's high-frequency polling script to instantly trigger the login modal
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

	// 4. Inject script at Document Start and Navigate to Bilibili
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

	// 5. High-frequency cookie polling
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
