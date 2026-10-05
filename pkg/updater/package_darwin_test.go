//go:build darwin

package updater

import (
	"bilibili_downloader/pkg/update"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func makeNativePackage(t *testing.T, root, oldBinary, newBinary string) (string, string) {
	t.Helper()
	install := filepath.Join(root, "安装 文件")
	if err := os.MkdirAll(install, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(install, "BBDown Pro.app")
	makeProbeBundle(t, target, oldBinary, "1.0.0", "com.example.bbdownupdatertest")
	payload := filepath.Join(root, "payload")
	candidate := filepath.Join(payload, "BBDown Pro.app")
	makeProbeBundle(t, candidate, newBinary, "1.0.1", "com.example.bbdownupdatertest")
	dmg := filepath.Join(root, "BBDown-Pro-macOS.dmg")
	cmd := exec.Command("/usr/bin/hdiutil", "create", "-format", "UDZO", "-srcfolder", payload, "-volname", "BBDown Update Test", dmg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create DMG: %v %s", err, out)
	}
	return target, dmg
}
func makeProbeBundle(t *testing.T, bundle, binary, version, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(binary, filepath.Join(bundle, "Contents", "MacOS", "BBDown Pro"), 0700); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleExecutable</key><string>BBDown Pro</string><key>CFBundleIdentifier</key><string>%s</string><key>CFBundleVersion</key><string>%s</string><key>CFBundleShortVersionString</key><string>%s</string><key>LSUIElement</key><true/></dict></plist>`, id, version, version)
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", bundle).CombinedOutput(); err != nil {
		t.Fatalf("sign test bundle: %v %s", err, out)
	}
}
func TestMacBundleIdentityAndVersionAreRequired(t *testing.T) {
	if os.Getenv("BBDOWN_NATIVE_UPDATE_TESTS") != "1" {
		t.Skip("native package fixtures opt-in")
	}
	root := t.TempDir()
	probe := buildNativeProbe(t, root, root, "1.0.1")
	current := filepath.Join(root, "old.app")
	candidate := filepath.Join(root, "new.app")
	makeProbeBundle(t, current, probe, "1.0.0", "com.example.old")
	makeProbeBundle(t, candidate, probe, "1.0.1", "com.example.different")
	if err := validateMacBundle(context.Background(), current, candidate, "1.0.1"); err == nil {
		t.Fatal("unrelated bundle accepted")
	}
	if _, err := Prepare(context.Background(), filepath.Join(root, "missing.dmg"), update.Release{Version: "1.0.1"}, Config{Directory: root, Executable: filepath.Join(current, "Contents", "MacOS", "BBDown Pro")}); err == nil {
		t.Fatal("missing package accepted")
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatal("preparation damaged current app", err)
	}
}
