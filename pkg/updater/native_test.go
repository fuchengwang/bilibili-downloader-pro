//go:build darwin || windows

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir = canonical
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
	for _, mode := range []string{"--schedule", "--restart"} {
		t.Run(mode, func(t *testing.T) { testNativeDownloadInstallAndLaunch(t, mode) })
	}
}

func testNativeDownloadInstallAndLaunch(t *testing.T, mode string) {
	t.Helper()
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
	cmd := exec.Command(executable, mode)
	quietCommand(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("schedule: %v %s", err, out)
	}
	helperPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || helperPID <= 0 {
		t.Fatalf("missing helper acknowledgement: %q %v", out, err)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitParent(ctx, helperPID); err != nil {
		t.Fatal("helper did not confirm startup and exit", err)
	}
}

func TestHelperReadinessRequiresThisProcessAndPlan(t *testing.T) {
	directory := t.TempDir()
	p := &Plan{Token: "this-plan", Phase: "scheduled", HelperPID: 123}
	if err := WritePlan(directory, p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		pid   int
	}{{"old-plan", 123}, {"this-plan", 124}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := waitHelperReady(ctx, directory, tc.token, tc.pid, make(chan error))
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("stale helper acknowledgement accepted", err)
		}
	}
	if err := waitHelperReady(context.Background(), directory, p.Token, p.HelperPID, make(chan error)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	done <- errors.New("helper was killed")
	if err := waitHelperReady(context.Background(), directory, p.Token, p.HelperPID, done); err == nil {
		t.Fatal("a dead helper allowed the old app to exit")
	}
}

func TestStartupConfirmationRequiresExactInstallation(t *testing.T) {
	directory := t.TempDir()
	p := &Plan{Token: "this-plan", Version: "1.0.1", BinaryHash: "this-binary"}
	for _, receipt := range []openedInstallation{
		{Token: "old-plan", Version: p.Version, BinaryHash: p.BinaryHash},
		{Token: p.Token, Version: "1.0.0", BinaryHash: p.BinaryHash},
		{Token: p.Token, Version: p.Version, BinaryHash: "old-binary"},
	} {
		raw, _ := json.Marshal(receipt)
		if err := os.WriteFile(filepath.Join(directory, "opened.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := waitApplicationOpened(ctx, directory, p)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("unrelated startup confirmed an update", err)
		}
	}
	raw, _ := json.Marshal(openedInstallation{Token: p.Token, Version: p.Version, BinaryHash: p.BinaryHash})
	if err := os.WriteFile(filepath.Join(directory, "opened.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitApplicationOpened(context.Background(), directory, p); err != nil {
		t.Fatal(err)
	}
}

func TestFailedInstallIsNotPresentedAsCompletedAfterReopening(t *testing.T) {
	for _, phase := range []string{"failed", "scheduled"} {
		t.Run(phase, func(t *testing.T) {
			directory := t.TempDir()
			p := nativeFixturePlan(t, t.TempDir())
			p.Phase = phase
			p.AutoApply = false
			if err := WritePlan(directory, p); err != nil {
				t.Fatal(err)
			}
			m, err := New(Config{Directory: directory, Executable: targetBinary(p), Version: "1.0.0", Client: &fakeClient{}})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if state := m.Snapshot(); state.Phase != "install_failed" || state.Error == "" || !state.HasUpdate {
				t.Fatal("failed installation still looks completed", state)
			}
		})
	}
}

func TestStartupRecoversReplacementInterruptedBeforeAppliedMarker(t *testing.T) {
	directory := t.TempDir()
	p := nativeFixturePlan(t, t.TempDir())
	p.Phase = "scheduled"
	if err := replaceInstallation(p); err != nil {
		t.Fatal(err)
	}
	if err := WritePlan(directory, p); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Directory: directory, Executable: targetBinary(p), Version: p.Version, Client: &fakeClient{}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.MarkOpened()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitApplicationOpened(ctx, directory, p); err != nil {
		t.Fatal("a verified new startup was not acknowledged", err)
	}
	if _, err := os.Stat(p.Backup); !os.IsNotExist(err) {
		t.Fatal("old backup was not cleaned after startup", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "install.json")); !os.IsNotExist(err) {
		t.Fatal("interrupted marker was not reconciled", err)
	}
}

func TestHelperRejectsPlanChangedAfterScheduling(t *testing.T) {
	directory := t.TempDir()
	p := nativeFixturePlan(t, t.TempDir())
	p.Phase = "scheduled"
	if err := WritePlan(directory, p); err != nil {
		t.Fatal(err)
	}
	if handled, err := HandleHelperArgs([]string{"--bbdown-apply-update", filepath.Join(directory, "install.json"), "a-different-plan"}); !handled || err == nil {
		t.Fatal("helper accepted a changed plan", handled, err)
	}
	if hash, _ := hashFile(preparedBinary(p)); hash != p.BinaryHash {
		t.Fatal("a rejected helper modified the update")
	}
}

func TestManualUpgradeDoesNotApplyOrDisplayAnOlderPendingUpdate(t *testing.T) {
	for _, phase := range []string{"ready", "scheduled", "applied", "failed"} {
		t.Run(phase, func(t *testing.T) {
			directory := t.TempDir()
			p := nativeFixturePlan(t, t.TempDir())
			p.Phase, p.AutoApply = phase, true
			if err := WritePlan(directory, p); err != nil {
				t.Fatal(err)
			}
			if handled, err := LaunchPending(directory, targetBinary(p), "1.0.2"); err != nil || handled {
				t.Fatal("a manually upgraded app tried to downgrade", handled, err)
			}
			if handled, err := ContinueInstallation(directory, "1.0.2"); err != nil || handled {
				t.Fatal("an obsolete installation intercepted the newer app", handled, err)
			}
			m, err := New(Config{Directory: directory, Executable: targetBinary(p), Version: "1.0.2", Client: &fakeClient{}})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if state := m.Snapshot(); state.HasUpdate || state.Error != "" || state.Phase != "idle" {
				t.Fatal("obsolete update was still displayed", state)
			}
			m.MarkOpened()
			if _, err := os.Stat(filepath.Join(directory, "install.json")); !os.IsNotExist(err) {
				t.Fatal("obsolete plan survived successful manual upgrade", err)
			}
			if _, err := os.Stat(p.Prepared); !os.IsNotExist(err) {
				t.Fatal("obsolete staging file survived successful manual upgrade", err)
			}
			if raw, err := os.ReadFile(targetBinary(p)); err != nil || string(raw) != "old executable" {
				t.Fatal("cleanup touched the running installation", err)
			}
		})
	}
}

func TestNativeHelperEarlyExitKeepsOldAppAndAllowsRetry(t *testing.T) {
	if os.Getenv("BBDOWN_NATIVE_UPDATE_TESTS") != "1" {
		t.Skip("native helpers opt-in")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "updates")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	oldBinary := buildNativeProbe(t, root, directory, "1.0.0")
	newBinary := buildNativeProbe(t, root, directory, "1.0.1")
	target, packagePath := makeNativePackage(t, root, oldBinary, newBinary)
	executable := target
	if runtime.GOOS == "darwin" {
		executable = filepath.Join(target, "Contents", "MacOS", "BBDown Pro")
	}
	cfg := Config{Directory: directory, Executable: executable, Version: "1.0.0", Client: &fakeClient{}}
	p, err := Prepare(context.Background(), packagePath, update.Release{Version: "1.0.1"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePlan(directory, p); err != nil {
		t.Fatal(err)
	}
	oldHash, _ := hashFile(executable)
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	t.Setenv("BBDOWN_TEST_HELPER_EXIT", "1")
	if err := m.LaunchInstall(); err == nil {
		t.Fatal("helper exit was treated as a successful restart")
	}
	if state := m.Snapshot(); state.Phase != "install_failed" || state.Error == "" {
		t.Fatal("helper failure was not visible", state)
	}
	failed, err := ReadPlan(directory)
	if err != nil || failed.AutoApply || failed.Phase != "failed" {
		t.Fatal("failed helper can repeat on startup", failed, err)
	}
	if hash, err := hashFile(executable); err != nil || hash != oldHash {
		t.Fatal("early helper failure changed the old application", err)
	}
	if handled, err := LaunchPending(directory, executable, "1.0.0"); err != nil || handled {
		t.Fatal("failed helper closed a reopened app", handled, err)
	}
	t.Setenv("BBDOWN_TEST_HELPER_EXIT", "")
	cmd := exec.Command(executable, "--restart")
	quietCommand(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("retry: %v %s", err, out)
	}
	helperPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := waitParent(ctx, helperPID); err != nil {
		t.Fatal("retried helper did not complete", err)
	}
	if err := checkExecutableVersion(context.Background(), executable, "1.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "install.json")); !os.IsNotExist(err) {
		t.Fatal("retry did not confirm the new app opened")
	}
}

func TestNativeNewAppEarlyExitRestoresAndReopensOldVersion(t *testing.T) {
	if os.Getenv("BBDOWN_NATIVE_UPDATE_TESTS") != "1" {
		t.Skip("native helpers opt-in")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "updates")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	oldBinary := buildNativeProbe(t, root, directory, "1.0.0")
	newBinary := buildNativeProbe(t, root, directory, "1.0.1", true)
	target, packagePath := makeNativePackage(t, root, oldBinary, newBinary)
	executable := target
	if runtime.GOOS == "darwin" {
		executable = filepath.Join(target, "Contents", "MacOS", "BBDown Pro")
	}
	p, err := Prepare(context.Background(), packagePath, update.Release{Version: "1.0.1"}, Config{Directory: directory, Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePlan(directory, p); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "--restart")
	quietCommand(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restart: %v %s", err, out)
	}
	helperPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := waitParent(ctx, helperPID); err != nil {
		log, _ := os.ReadFile(filepath.Join(directory, "install.log"))
		t.Fatalf("helper did not restore the old app: %v %s", err, log)
	}
	if err := checkExecutableVersion(context.Background(), executable, "1.0.0"); err != nil {
		t.Fatal("old version was not restored", err)
	}
	failed, err := ReadPlan(directory)
	if err != nil || failed.Phase != "failed" || failed.AutoApply || failed.Error == "" {
		t.Fatal("startup failure did not become a retryable failure", failed, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(filepath.Join(directory, "opened"))
		if string(raw) == "1.0.0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restored old version was not reopened")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func buildNativeProbe(t *testing.T, root, directory, version string, failStartup ...bool) string {
	t.Helper()
	source := fmt.Sprintf(`package main
import("context";"fmt";"os";"path/filepath";"bilibili_downloader/pkg/updater";"bilibili_downloader/pkg/update")
var version string
var failStartup string
const directory=%q
type client struct{}
func(client)Check(context.Context)(*update.CheckResult,error){return &update.CheckResult{},nil}
func(client)Download(context.Context,update.Release,update.DownloadOptions)(string,error){return "",nil}
func(client)Confirm(context.Context,update.Release)error{return nil}
func main(){
 if len(os.Args)==2&&os.Args[1]=="--bbdown-version"{fmt.Println(version);return}
 if len(os.Args)>1&&os.Args[1]=="--bbdown-apply-update"&&os.Getenv("BBDOWN_TEST_HELPER_EXIT")=="1"{os.Exit(9)}
 if handled,err:=updater.HandleHelperArgs(os.Args[1:]);handled{if err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(1)};return}
 if failStartup=="1"&&len(os.Args)==1{os.Exit(8)}
 executable,_:=os.Executable()
 if len(os.Args)==2&&os.Args[1]=="--schedule"{handled,err:=updater.LaunchPending(directory,executable,version);if err!=nil||!handled{fmt.Fprintln(os.Stderr,handled,err);os.Exit(2)};p,_:=updater.ReadPlan(directory);fmt.Println(p.HelperPID);return}
 manager,err:=updater.New(updater.Config{Directory:directory,Version:version,Executable:executable,Client:client{}});if err!=nil{panic(err)}
 if len(os.Args)==2&&os.Args[1]=="--restart"{if err:=manager.LaunchInstall();err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(3)};p,_:=updater.ReadPlan(directory);fmt.Println(p.HelperPID);manager.Close();return}
 manager.MarkOpened();manager.Close()
 _ = os.WriteFile(filepath.Join(directory,"opened"),[]byte(version),0600)
}`, directory)
	sourcePath := filepath.Join(root, "probe.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "probe-"+version+helperSuffix())
	flags := "-X main.version=" + version
	if len(failStartup) > 0 && failStartup[0] {
		flags += " -X main.failStartup=1"
	}
	cmd := exec.Command("go", "build", "-ldflags", flags, "-o", binary, sourcePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native probe: %v %s", err, out)
	}
	return binary
}
