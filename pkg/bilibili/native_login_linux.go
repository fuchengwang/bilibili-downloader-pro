//go:build linux

package bilibili

import (
	"context"
	"fmt"
)

// OpenNativeBrowserLogin on Linux is not yet implemented natively.
// We fallback to informing the user.
func (c *Client) OpenNativeBrowserLogin(ctx context.Context) error {
	return fmt.Errorf("Linux 环境下请使用「扫码登录」或手动提取 Cookie")
}
