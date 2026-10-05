//go:build !darwin && !windows

package updater

import (
	"bilibili_downloader/pkg/update"
	"context"
	"errors"
	"os/exec"
)

var errUnsupported = errors.New("当前系统暂不支持应用内安装更新")

func installationTarget(string) (string, error) { return "", errUnsupported }
func preparePackage(context.Context, string, update.Release, *Plan, string) error {
	return errUnsupported
}
func validatePlatformPlan(*Plan) error      { return errUnsupported }
func preparedBinary(p *Plan) string         { return p.Prepared }
func helperSuffix() string                  { return "" }
func detachCommand(*exec.Cmd)               {}
func quietCommand(*exec.Cmd)                {}
func waitParent(context.Context, int) error { return errUnsupported }
func replaceInstallation(*Plan) error       { return errUnsupported }
func restoreInstallation(*Plan) error       { return errUnsupported }
func launchApplication(string) error        { return errUnsupported }
func lockInstaller(string) (func(), error)  { return nil, errUnsupported }
