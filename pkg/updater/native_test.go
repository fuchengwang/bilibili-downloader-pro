//go:build darwin || windows

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bilibili_downloader/pkg/update"
)

func TestNativeAtomicReplaceRestoresCompleteOldInstallation(t *testing.T) {
	dir := t.TempDir()
	p := nativeFixturePlan(t, dir)
	if err := replaceInstallation(p); err != nil {
		t.Fatal(err)
	}
	if hash, _ := hashFile(targetBinary(p)); hash != p.BinaryHash {
		t.Fatal("replacement was not complete")
	}
	if err := restoreInstallation(p); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(targetBinary(p)); err != nil || string(raw) != "old executable" {
		t.Fatal("old installation not restored", err)
	}
	if raw, err := os.ReadFile(preparedBinary(p)); err != nil || string(raw) != "new executable" {
		t.Fatal("candidate no longer retryable", err)
	}
}
func TestNativeFailedReplacementKeepsOriginal(t *testing.T) {
	p := nativeFixturePlan(t, t.TempDir())
	_ = os.RemoveAll(p.Prepared)
	if err := replaceInstallation(p); err == nil {
		t.Fatal("missing candidate succeeded")
	}
	if raw, err := os.ReadFile(targetBinary(p)); err != nil || string(raw) != "old executable" {
		t.Fatal("failed update lost old executable", err)
	}
}
func TestInstallerLockReleasesWithoutDeletingLockFile(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockInstaller(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := lockInstaller(dir); err == nil {
		again()
		t.Fatal("concurrent installer allowed")
	}
	unlock()
	again, err := lockInstaller(dir)
	if err != nil {
		t.Fatal("stale lock after exit", err)
	}
	again()
}

func TestSecondOldLaunchDoesNotInterfereWithActiveInstaller(t *testing.T) {
	dir := t.TempDir()
	p := nativeFixturePlan(t, t.TempDir())
	p.Phase = "scheduled"
	if err := WritePlan(dir, p); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockInstaller(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if handled, err := ContinueInstallation(dir, "1.0.0"); err != nil || !handled {
		t.Fatal("second old process was allowed to hold the installed executable", handled, err)
	}
	if handled, err := ContinueInstallation(dir, "1.0.1"); err != nil || handled {
		t.Fatal("new app was blocked from opening", handled, err)
	}
}

func TestSchedulingFailureIsRetryableAndDisablesAutomaticLoop(t *testing.T) {
	directory := t.TempDir()
	p := nativeFixturePlan(t, t.TempDir())
	p.AutoApply = true
	if err := WritePlan(directory, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "helper"), []byte("blocked directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LaunchInstall(directory, targetBinary(p)); err == nil {
		t.Fatal("blocked helper directory accepted")
	}
	failed, err := ReadPlan(directory)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Phase != "failed" || failed.AutoApply || failed.Error == "" {
		t.Fatal("failed scheduling still repeats automatically", failed)
	}
	if handled, err := LaunchPending(directory, targetBinary(p), "1.0.0"); err != nil || handled {
		t.Fatal("failed installation repeated on startup", handled, err)
	}
	if raw, err := os.ReadFile(targetBinary(p)); err != nil || string(raw) != "old executable" {
		t.Fatal("scheduling failure damaged old app", err)
	}
}

func nativeFixturePlan(t *testing.T, dir string) *Plan {
	t.Helper()
	token := strings.Repeat("a", 32)
	p := &Plan{Token: token, Version: "1.0.1", Phase: "ready"}
	if runtime.GOOS == "darwin" {
		p.Target = filepath.Join(dir, "BBDown Pro.app")
		p.Prepared = filepath.Join(dir, ".bbdown-update-"+token+".app")
		p.Backup = p.Prepared
		p.BinaryRelative = filepath.Join("Contents", "MacOS", "BBDown Pro")
		for _, root := range []string{p.Target, p.Prepared} {
			if err := os.MkdirAll(filepath.Join(root, "Contents", "MacOS"), 0700); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		p.Target = filepath.Join(dir, "BBDown Pro.exe")
		p.Prepared = filepath.Join(dir, ".bbdown-update-"+token+".exe")
		p.Backup = filepath.Join(dir, ".bbdown-previous-"+token+".exe")
	}
	if err := os.WriteFile(targetBinary(p), []byte("old executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preparedBinary(p), []byte("new executable"), 0700); err != nil {
		t.Fatal(err)
	}
	p.BinaryHash, _ = hashFile(preparedBinary(p))
	if err := validatePlan(p, dir); err != nil {
		t.Fatal(err)
	}
	return p
}

// Opt-in because this builds native probes, creates/mounts a real DMG on macOS,
// and launches a headless fixture through the exact production helper path.
// It never updates the installed BBDown application or user profile.
func TestNativeDownloadDeferredInstallAndLaunch(t *testing.T) {
	if os.Getenv("BBDOWN_NATIVE_UPDATE_TESTS") != "1" {
		t.Skip("set BBDOWN_NATIVE_UPDATE_TESTS=1 for real package/helper installation")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "user data", "updates")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	oldBinary := buildNativeProbe(t, root, directory, "1.0.0")
	newBinary := buildNativeProbe(t, root, directory, "1.0.1")
	target, packagePath := makeNativePackage(t, root, oldBinary, newBinary)
	contents, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	var release update.Release
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/package" {
			w.Write(contents)
			return
		}
		if r.URL.Path != "/api/v1/updates/check" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(update.Response[update.CheckResult]{Code: 200, Success: true, Data: update.CheckResult{AppID: "bbdown-pro", CurrentVersion: "1.0.0", HasUpdate: true, Release: &release}})
	}))
	defer server.Close()
	release = update.Release{Version: "1.0.1", Notes: "native integration test", Artifact: update.Artifact{OS: runtime.GOOS, Arch: runtime.GOARCH, FileName: filepath.Base(packagePath), Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:]), Sources: []update.Source{{URL: server.URL + "/package"}}}}
	client, err := update.New(update.Config{ServerURL: server.URL, AppID: "bbdown-pro", CurrentVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	executable := target
	if runtime.GOOS == "darwin" {
		executable = filepath.Join(target, "Contents", "MacOS", "BBDown Pro")
	}
	m, err := New(Config{Directory: directory, Version: "1.0.0", Executable: executable, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	if err := m.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	if m.Snapshot().Phase != "ready" {
		t.Fatal(m.Snapshot())
	}
	p, err := ReadPlan(directory)
	if err != nil {
		t.Fatal(err)
	}
	// Deferring/dismissing only leaves this ready plan; opening the old app on
	// the next occasion starts the helper, exits, replaces, and opens the new one.
	oldHash, _ := hashFile(executable)
	if oldHash == p.BinaryHash {
		t.Fatal("download replaced a running installation")
	}
	cmd := exec.Command(executable, "--schedule")
	quietCommand(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("schedule: %v %s", err, out)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, _ := os.ReadFile(filepath.Join(directory, "opened"))
		if string(raw) == "1.0.1" {
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(directory, "install.log"))
			plan, _ := os.ReadFile(filepath.Join(directory, "install.json"))
			t.Fatalf("new app did not open; log=%s plan=%s", log, plan)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := checkExecutableVersion(context.Background(), executable, "1.0.1"); err != nil {
		t.Fatal(err)
	}
	// MarkOpened completes cleanup only after the new process actually starts.
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(directory, "install.json")); os.IsNotExist(err) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(p.Backup); !os.IsNotExist(err) {
		t.Fatal("successful startup did not clean the old backup", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "install.json")); !os.IsNotExist(err) {
		t.Fatal("pending marker survived successful launch")
	}
}

func buildNativeProbe(t *testing.T, root, directory, version string) string {
	t.Helper()
	source := fmt.Sprintf(`package main
import("context";"fmt";"os";"path/filepath";"bilibili_downloader/pkg/updater";"bilibili_downloader/pkg/update")
var version string
const directory=%q
type client struct{}
func(client)Check(context.Context)(*update.CheckResult,error){return &update.CheckResult{},nil}
func(client)Download(context.Context,update.Release,update.DownloadOptions)(string,error){return "",nil}
func(client)Confirm(context.Context,update.Release)error{return nil}
func main(){
 if len(os.Args)==2&&os.Args[1]=="--bbdown-version"{fmt.Println(version);return}
 if handled,err:=updater.HandleHelperArgs(os.Args[1:]);handled{if err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(1)};return}
 executable,_:=os.Executable()
 if len(os.Args)==2&&os.Args[1]=="--schedule"{handled,err:=updater.LaunchPending(directory,executable,version);if err!=nil||!handled{fmt.Fprintln(os.Stderr,handled,err);os.Exit(2)};return}
 manager,err:=updater.New(updater.Config{Directory:directory,Version:version,Executable:executable,Client:client{}});if err!=nil{panic(err)}
 manager.MarkOpened();manager.Close()
 _ = os.WriteFile(filepath.Join(directory,"opened"),[]byte(version),0600)
}`, directory)
	sourcePath := filepath.Join(root, "probe.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "probe-"+version+helperSuffix())
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", binary, sourcePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native probe: %v %s", err, out)
	}
	return binary
}
