package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bilibili_downloader/pkg/downloader"
	"bilibili_downloader/pkg/license"
)

type fakeLicenseChecker struct {
	status      *license.LicenseStatus
	checkErr    error
	activateErr error
}

func (f *fakeLicenseChecker) CheckStatus(context.Context, bool) (*license.LicenseStatus, error) {
	return f.status, f.checkErr
}

func (f *fakeLicenseChecker) Activate(context.Context, string) (*license.LicenseStatus, error) {
	return f.status, f.activateErr
}

func (f *fakeLicenseChecker) Deactivate(context.Context) error {
	return f.activateErr
}

func TestCoreDownloadEntryPointsRejectUnlicensedCalls(t *testing.T) {
	app := &App{license: &fakeLicenseChecker{status: &license.LicenseStatus{Message: "应用尚未激活"}}}

	if _, err := app.ParseURL("BV1xx"); err == nil || !strings.Contains(err.Error(), "尚未激活") {
		t.Fatalf("ParseURL should reject an unlicensed call, got %v", err)
	}
	if _, err := app.GetAvailableQualities("BV1xx", 1, 2, 0, false, false); err == nil {
		t.Fatal("GetAvailableQualities should reject an unlicensed call")
	}
	if _, err := app.GetPlaybackInfo("BV1xx", 1, 2, 0, false, false); err == nil {
		t.Fatal("GetPlaybackInfo should reject an unlicensed call")
	}
	if _, err := app.AddDownloadTasks(downloader.DownloadRequest{}); err == nil {
		t.Fatal("AddDownloadTasks should reject an unlicensed call")
	}
	if err := app.ResumeTask("task-id"); err == nil {
		t.Fatal("ResumeTask should reject an unlicensed call")
	}
	if err := app.ResumeAllTasks(); err == nil {
		t.Fatal("ResumeAllTasks should reject an unlicensed call")
	}
}

func TestLicenseErrorsAreReturnedToActivationScreen(t *testing.T) {
	app := &App{license: &fakeLicenseChecker{
		status:      &license.LicenseStatus{Message: "卡密已吊销"},
		checkErr:    errors.New("revoked"),
		activateErr: errors.New("卡密不存在"),
	}}

	checked := app.CheckLicense(false)
	if checked.Success || checked.Message != "卡密已吊销" {
		t.Fatalf("unexpected check result: %+v", checked)
	}
	activated := app.ActivateLicense("invalid")
	if activated.Success || activated.Message != "卡密不存在" {
		t.Fatalf("unexpected activation result: %+v", activated)
	}
}

func TestDeactivateFailureKeepsApplicationLicensed(t *testing.T) {
	app := &App{license: &fakeLicenseChecker{activateErr: errors.New("暂时无法连接服务器")}}
	result := app.DeactivateLicense()
	if result.Success || result.Message != "暂时无法连接服务器" {
		t.Fatalf("unexpected deactivation result: %+v", result)
	}
}

func TestDeactivateSuccessReturnsToActivationFlow(t *testing.T) {
	app := &App{license: &fakeLicenseChecker{}}
	result := app.DeactivateLicense()
	if !result.Success || result.Message != "本机已解绑" {
		t.Fatalf("unexpected deactivation result: %+v", result)
	}
}
