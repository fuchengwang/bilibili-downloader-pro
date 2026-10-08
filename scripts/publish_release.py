# /// script
# requires-python = ">=3.12"
# dependencies = ["requests==2.32.5", "playwright==1.58.0"]
# ///
"""Build and distribute verified releases to GitCode and Lanzou."""

from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.parse import quote, urlparse

import requests

ROOT = Path(__file__).resolve().parents[1]
PACKAGES = {
    "windows": (
        "BBDown-Pro-Windows-amd64.zip",
        "Windows版 BBDown Pro{version}.zip",
        "amd64",
    ),
    "darwin": (
        "BBDown-Pro-macOS-universal.dmg",
        "MacOS版 BBDown Pro{version}.dmg",
        "universal",
    ),
}
OLD_NAME = re.compile(r"^(Windows版|MacOS版) BBDown Pro(\d+\.\d+\.\d+)\.(zip|dmg)$")
EVENTS = False


def emit(step, status, message, **fields):
    """A small, versioned protocol for the desktop publisher; no credentials."""
    if EVENTS:
        print("BBDOWN_EVENT " + json.dumps(dict(protocol=1, step=step, status=status,
              message=message, **fields), ensure_ascii=False), flush=True)


class PublishError(Exception):
    pass


def version_tuple(version):
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise PublishError("版本号必须为 X.Y.Z")
    return tuple(map(int, version.split(".")))


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def verify_file(path, expected):
    if not Path(path).is_file() or digest(path) != expected:
        raise PublishError(f"安装包校验失败：{Path(path).name}")


def older_packages(names, version):
    """Only exact BBDown package names of an older version can be deleted."""
    target = version_tuple(version)
    candidates = []
    for name in names:
        match = OLD_NAME.fullmatch(name)
        if (
            match
            and (
                (match[1] == "Windows版" and match[3] == "zip")
                or (match[1] == "MacOS版" and match[3] == "dmg")
            )
            and version_tuple(match[2]) < target
        ):
            candidates.append(name)
    return sorted(set(candidates))


def json_write(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    tmp.chmod(0o600)
    tmp.replace(path)


def command(*args):
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True, check=False)
    if result.returncode:
        # CLI stderr can contain signed URLs or authentication diagnostics.
        raise PublishError(
            f"命令失败：{' '.join(args[:3])}（退出码 {result.returncode}）"
        )
    return result.stdout.strip()


def safe_error(error):
    if isinstance(error, PublishError):
        return str(error)
    return type(error).__name__ + "（请检查登录、网络或本地截图）"


def trigger_build(version, expected_commit=None):
    tag = "v" + version
    if command("git", "status", "--porcelain"):
        raise PublishError("请先提交发布改动")
    tags = command("git", "tag", "--list", tag)
    if tags:
        raise PublishError(
            f"标签 {tag} 已存在，补做分发请加 --resume；重建请运行 "
            f"gh workflow run release.yml -f tag={tag}"
        )
    commit = command("git", "rev-parse", "HEAD")
    if expected_commit and commit != expected_commit:
        raise PublishError("代码提交已变化，请重新检查后开始发布")
    branch = command("git", "symbolic-ref", "--short", "HEAD")
    command("git", "push", "origin", f"{commit}:refs/heads/{branch}")
    command("git", "tag", "-a", tag, commit, "-m", "BBDown Pro " + tag)
    command("git", "push", "origin", tag)
    print("已触发正式构建：" + tag, flush=True)


def verify_origin_tag(tag, commit):
    refs = command("git", "ls-remote", "origin", "refs/tags/" + tag, "refs/tags/" + tag + "^{}")
    targets = dict(line.split(None, 1)[::-1] for line in refs.splitlines() if line.strip())
    remote = targets.get("refs/tags/" + tag + "^{}", targets.get("refs/tags/" + tag))
    if remote != commit:
        raise PublishError("GitHub 上的版本标签与本次提交不一致，停止分发")


def run_stage(name, operation, state, save, retry=False):
    """Keep independent channels running after a failure and persist its boundary."""
    stages = state.setdefault("stages", {})
    if stages.get(name, {}).get("status") == "complete" and not retry:
        emit(name, "complete", "上次已完成，本次复用结果")
        print(f"{name} 已完成，跳过；需要重新校验可用 --stage {name}", flush=True)
        return True
    stages[name] = {"status": "running"}
    save()
    emit(name, "running", "正在执行")
    try:
        operation()
    except Exception as error:  # noqa: BLE001 - channel boundary must preserve partial results.
        stages[name] = {"status": "failed", "error": safe_error(error)}
        save()
        emit(name, "failed", safe_error(error))
        print(f"{name} 未完成：{safe_error(error)}；继续其他渠道。", flush=True)
        return False
    stages[name] = {"status": "complete"}
    save()
    emit(name, "complete", "已完成并验证")
    return True


def updater_requested(stage, config):
    return stage == "updater" or (
        stage == "all" and config.get("updater", {}).get("enabled", False)
    )


def wait_until(operation, timeout, description, interval=2):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = operation()
        if result:
            return result
        time.sleep(interval)
    raise PublishError(f"等待超时：{description}")


def public_url(value):
    parsed = urlparse(value)
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username
        or parsed.password
    ):
        raise PublishError("下载地址必须为 HTTPS URL")
    return value


def verify_url(url, expected_hash, expected_size):
    with requests.get(public_url(url), stream=True, timeout=(20, 120)) as response:
        response.raise_for_status()
        hasher, size = hashlib.sha256(), 0
        for data in response.iter_content(1024 * 1024):
            size += len(data)
            if size > expected_size:
                raise PublishError("下载文件大小超过预期")
            hasher.update(data)
        if size != expected_size or hasher.hexdigest() != expected_hash:
            raise PublishError("下载文件与正式构建包不一致")


class GitCode:
    def __init__(self, repo, token):
        if not re.fullmatch(r"[\w.-]+/[\w.-]+", repo):
            raise PublishError("GitCode 仓库格式应为 owner/repo")
        self.base = "https://api.gitcode.com/api/v5/repos/" + repo
        self.token = token

    def api(self, method, suffix, allow_missing=False, **kwargs):
        params = dict(kwargs.pop("params", {}), access_token=self.token)
        response = requests.request(
            method, self.base + suffix, params=params, timeout=(20, 120), **kwargs
        )
        if allow_missing and response.status_code == 404:
            return None
        response.raise_for_status()
        result = response.json()
        if isinstance(result, dict) and result.get("success") is False:
            raise PublishError("GitCode 请求未成功")
        return result

    def release(self, tag):
        return self.api("GET", "/releases/" + quote(tag, safe=""), allow_missing=True)

    def publish(self, tag, commit, notes, files):
        release = self.release(tag)
        if release is None:
            release = self.api(
                "POST",
                "/releases",
                json={
                    "tag_name": tag,
                    "name": "BBDown Pro " + tag,
                    "body": notes,
                    "target_commitish": commit,
                    "release_status": "pre",
                },
            )
        results = {}
        for path in files:
            assets = (self.release(tag) or {}).get("assets", [])
            matches = [a for a in assets if a.get("name") == path.name]
            if len(matches) > 1:
                raise PublishError(f"GitCode 存在重复附件：{path.name}")
            if not matches:
                upload = self.api(
                    "GET",
                    "/releases/" + quote(tag, safe="") + "/upload_url",
                    params={"file_name": path.name},
                )
                with path.open("rb") as stream:
                    response = requests.put(
                        public_url(upload["url"]),
                        headers=upload["headers"],
                        data=stream,
                        timeout=(20, 300),
                    )
                response.raise_for_status()

            def find_asset(path=path):
                return next(
                    (
                        a.get("browser_download_url")
                        for a in (self.release(tag) or {}).get("assets", [])
                        if a.get("name") == path.name
                    ),
                    None,
                )

            url = wait_until(find_asset, 90, f"GitCode 附件 {path.name}")
            verify_url(url, digest(path), path.stat().st_size)
            results[path.name] = url
            print(f"GitCode 已验证：{path.name}", flush=True)
        self.api(
            "PATCH",
            "/releases/" + quote(tag, safe=""),
            json={
                "name": "BBDown Pro " + tag,
                "body": notes,
                "release_status": "latest",
            },
        )
        return results


class UpdateServer:
    def __init__(self, settings, token):
        self.base = public_url(settings["base_url"]).rstrip("/") + "/api/v1/admin"
        self.settings = settings
        self.headers = {"Authorization": "Bearer " + token}

    def api(self, method, path, **kwargs):
        response = requests.request(
            method, self.base + path, headers=self.headers, timeout=(20, 60), **kwargs
        )
        response.raise_for_status()
        result = response.json()
        if result.get("success") is not True:
            raise PublishError("更新后台：" + str(result.get("message", "请求未成功")))
        return result["data"]

    def publish(self, version, notes, files, urls):
        emit("updater", "running", "核对云端已有版本，准备更新草稿", link=self.settings["base_url"])
        artifacts = [
            {
                "os": platform,
                "arch": spec[2],
                "file_name": spec[0],
                "size": files[platform].stat().st_size,
                "sha256": digest(files[platform]),
                "sources": [{"name": "GitCode", "url": urls[spec[0]]}],
            }
            for platform, spec in PACKAGES.items()
        ]
        payload = {
            "app_id": self.settings.get("app_id", "bbdown-pro"),
            "version": version,
            "notes": notes,
            "artifacts": artifacts,
        }
        existing = None
        for page in range(1, 101):
            result = self.api(
                "GET",
                "/releases",
                params={"app_id": payload["app_id"], "page": page, "size": 100},
            )
            existing = next(
                (r for r in result["list"] if r["version"] == version), None
            )
            if existing or page * 100 >= result["total"]:
                break
        if existing:
            release = self.api("GET", f"/releases/{existing['id']}")
            if release["artifacts"] != artifacts:
                if release.get("published_at") or release["status"] != "draft":
                    raise PublishError(
                        "更新后台已有该版本，安装包或下载源不同；请使用新版本号"
                    )
                if release["check_status"] == "checking":
                    raise PublishError("更新后台正在试下载，请稍后重试")
                release = self.api("PUT", f"/releases/{release['id']}", json=payload)
        else:
            release = self.api("POST", "/releases", json=payload)
        path = f"/releases/{release['id']}"
        if release["status"] == "withdrawn":
            raise PublishError("该版本已撤回，不自动重新发布")
        if release["status"] == "published":
            return release
        emit("updater", "running", "安装包信息已登记，等待服务器试下载和 SHA-256 校验")
        if release["check_status"] not in ("passed", "checking"):
            self.api("POST", path + "/test-download", json={"force": False})

        def checked():
            current = self.api("GET", path)
            return current if current["check_status"] in ("passed", "failed") else None

        release = wait_until(checked, 1800, "更新后台试下载", interval=5)
        if release["check_status"] != "passed":
            raise PublishError("更新后台试下载未通过，草稿保留供重试")
        if self.settings.get("publish", True):
            emit("updater", "running", "服务器试下载通过，正在正式发布")
            release = self.api("POST", path + "/publish")
        return release


def complete_lanzou(browser, packages, version, save_result):
    """The deletion barrier is crossed only after BOTH anonymous downloads match."""
    results = {}
    for platform, path in packages.items():
        emit("lanzou", "running", "上传：" + path.name)
        browser.ensure_upload(path)
    for platform, path in packages.items():
        url = browser.share_url(path.name)
        emit("lanzou", "running", "匿名下载校验：" + path.name)
        browser.verify_download(url, path)
        results[platform] = {"name": path.name, "url": url, "sha256": digest(path)}
        save_result(results)
    if set(results) != set(PACKAGES):
        raise PublishError("两个平台未全部验证，保留旧版本")
    for name in older_packages(browser.file_names(), version):
        if not all(path.name in browser.file_names() for path in packages.values()):
            raise PublishError("新版安装包已不在文件夹中，停止删除旧版")
        browser.delete(name)
        print(f"旧版已移入蓝奏云回收站：{name}", flush=True)
    return results


def prepare_assets(repo, tag, directory, timeout):
    commit = command("git", "rev-parse", tag + "^{commit}")

    def ready():
        raw = command(
            "gh",
            "release",
            "view",
            tag,
            "--repo",
            repo,
            "--json",
            "assets,isDraft,body",
        )
        release = json.loads(raw)
        names = {a["name"] for a in release["assets"]}
        return (
            release
            if not release["isDraft"]
            and set([s[0] for s in PACKAGES.values()] + ["SHA256SUMS.txt"]) <= names
            else None
        )

    # gh exits nonzero until the release exists; distinguish auth/network failures first.
    command("gh", "auth", "status")

    def available():
        try:
            release = ready()
        except PublishError:
            release = None
        if release:
            return release
        runs = json.loads(
            command(
                "gh",
                "run",
                "list",
                "--repo",
                repo,
                "--workflow",
                "release.yml",
                "--limit",
                "20",
                "--json",
                "headSha,headBranch,status,conclusion,databaseId,url",
            )
        )
        latest = next(
            (
                run
                for run in runs
                if run["headSha"] == commit and run["headBranch"] == tag
            ),
            None,
        )
        if latest:
            emit("build", "running", "GitHub 构建：" + latest["status"], link=latest.get("url", ""))
            if latest.get("databaseId"):
                details = json.loads(command("gh", "run", "view", str(latest["databaseId"]),
                                           "--repo", repo, "--json", "jobs"))
                for job in details.get("jobs", []):
                    emit("build", "running", f"{job['name']}：{job['conclusion'] or job['status']}")
        if (
            latest
            and latest["status"] == "completed"
            and latest["conclusion"] != "success"
        ):
            raise PublishError(
                "正式构建未成功，不分发安装包；请检查 GitHub Actions 后重试"
            )
        return None

    print("等待 Windows ZIP 和已签名公证的 macOS DMG 正式发布……", flush=True)
    emit("build", "running", "等待双平台构建、macOS 签名和公证", link=f"https://github.com/{repo}/actions")
    release = wait_until(available, timeout, "GitHub 正式 Release", interval=15)
    emit("build", "complete", "双平台正式安装包已生成")
    emit("assets", "running", "下载正式校验清单")
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)

    def download(name):
        # A failed transfer never leaves a partial file in the reusable cache.
        with tempfile.TemporaryDirectory(prefix="asset-", dir=directory) as temp:
            command(
                "gh",
                "release",
                "download",
                tag,
                "--repo",
                repo,
                "--pattern",
                name,
                "--dir",
                temp,
            )
            Path(temp, name).replace(directory / name)

    # Refresh the manifest even when resuming; the recorded release must not drift.
    download("SHA256SUMS.txt")
    sums = {}
    for line in (directory / "SHA256SUMS.txt").read_text().splitlines():
        match = re.fullmatch(r"([0-9a-fA-F]{64})\s+\*?(?:\./)?([^/]+)", line)
        if match:
            sums[match[2]] = match[1].lower()
    files = {}
    for platform, spec in PACKAGES.items():
        emit("assets", "running", "下载并校验：" + spec[0])
        if spec[0] not in sums:
            raise PublishError("正式 Release 校验清单缺少安装包")
        path = directory / spec[0]
        if not path.is_file() or digest(path) != sums[spec[0]]:
            download(spec[0])
        verify_file(path, sums[spec[0]])
        files[platform] = path
    emit("assets", "complete", "Windows ZIP / macOS DMG 的 SHA-256 与正式清单一致")
    return files, release["body"]


def main():
    global EVENTS
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", nargs="?", help="可选版本号，必须与 wails.json 一致")
    parser.add_argument("--config", type=Path, default=ROOT / ".local/publish.json")
    parser.add_argument(
        "--resume", action="store_true", help="继续已触发的版本，不重复构建"
    )
    parser.add_argument(
        "--login-lanzou", action="store_true", help="打开独立浏览器配置首次登录"
    )
    parser.add_argument(
        "--login-gitcode", action="store_true", help="保存 GitCode 网页发布登录"
    )
    parser.add_argument(
        "--verify-lanzou",
        action="store_true",
        help="只读检查固定分享入口的当前版本，不上传或删除",
    )
    parser.add_argument(
        "--plan", action="store_true", help="只显示计划，不上传、发布或删除"
    )
    parser.add_argument(
        "--stage", choices=["all", "gitcode", "lanzou", "updater"], default="all"
    )
    parser.add_argument("--timeout", type=int, default=7200)
    parser.add_argument("--events", action="store_true", help="输出桌面发布器进度事件")
    parser.add_argument("--expected-commit", help="固定本次发布的提交")
    parser.add_argument("--notes-file", type=Path, help="发布说明 UTF-8 文件")
    parser.add_argument(
        "--visible", action="store_true", help="使用可见 Chrome 处理网页验证"
    )
    args = parser.parse_args()
    EVENTS = args.events
    if not args.config.is_file():
        raise PublishError(
            "请先复制 scripts/publish.example.json 到 .local/publish.json 并填写配置"
        )
    config = json.loads(args.config.read_text())
    if args.visible:
        config["lanzou"]["headless"] = False
    current_version = json.loads((ROOT / "wails.json").read_text())["info"]["productVersion"]
    version = args.version.removeprefix("v") if args.version else current_version
    version_tuple(version)
    if not args.resume and version != current_version:
        raise PublishError("参数版本必须与 wails.json 的 productVersion 一致")
    tag = "v" + version
    directory = ROOT / ".local/publish" / tag
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (ROOT / ".local/publish/lock").open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise PublishError("另一个发布脚本正在运行")
        from lanzou_browser import LanzouBrowser

        if args.login_lanzou:
            with LanzouBrowser(dict(config["lanzou"], headless=False), ROOT) as browser:
                browser.login()
            return
        if args.login_gitcode:
            from gitcode_browser import GitCodeBrowser

            with LanzouBrowser(dict(config["lanzou"], headless=False), ROOT) as browser:
                GitCodeBrowser(browser, config["gitcode_repo"]).login()
            return
        if args.plan:
            print(
                json.dumps(
                    {
                        "version": version,
                        "packages": [
                            s[1].format(version=version) for s in PACKAGES.values()
                        ],
                        "folder": config["lanzou"]["folder_path"],
                        "cleanup": "两个新版包全部匿名下载并校验后，移除该文件夹中更低版本的 BBDown 包",
                        "stage": args.stage,
                        "source_sync": "gitcode main 分支及本次标签（SSH）",
                        "updater": updater_requested(args.stage, config),
                    },
                    ensure_ascii=False,
                    indent=2,
                )
            )
            return
        if args.verify_lanzou:
            files, _ = prepare_assets(
                config["github_repo"], tag, directory / "assets", args.timeout
            )
            with tempfile.TemporaryDirectory(prefix="bbdown-public-check-") as temp:
                settings = dict(config["lanzou"], profile=temp + "/profile")
                with LanzouBrowser(settings, ROOT) as browser:
                    for platform, path in files.items():
                        url = browser.share_url(
                            PACKAGES[platform][1].format(version=version)
                        )
                        browser.verify_download(url, path)
            return
        if not args.resume:
            if args.stage != "all":
                raise PublishError("单独补做某一步时请加 --resume")
            emit("build", "running", "推送固定提交和版本标签，触发 GitHub 正式构建")
            trigger_build(version, args.expected_commit)
        commit = command("git", "rev-parse", tag + "^{commit}")
        if args.expected_commit and commit != args.expected_commit:
            raise PublishError("版本标签与本次任务固定的提交不一致")
        source = json.loads(command("git", "show", tag + ":wails.json"))
        if source["info"]["productVersion"] != version:
            raise PublishError("版本标签对应的程序版本不一致")
        state_path = directory / "state.json"
        state = (
            json.loads(state_path.read_text())
            if state_path.exists()
            else {"version": version, "commit": commit}
        )
        if state["commit"] != commit:
            raise PublishError("版本标签对应的提交发生变化")
        save = lambda: json_write(state_path, state)
        save()  # Preserve the identity even if building or downloading is interrupted.
        if args.resume:
            command("git", "push", "origin", tag)  # Also recover a local tag whose push failed.
        verify_origin_tag(tag, commit)
        files, notes = prepare_assets(
            config["github_repo"], tag, directory / "assets", args.timeout
        )
        if args.notes_file:
            notes = args.notes_file.read_text(encoding="utf-8")
            command("gh", "release", "edit", tag, "--repo", config["github_repo"],
                    "--notes-file", str(args.notes_file))
        assets = {
            p.name: {"sha256": digest(p), "size": p.stat().st_size}
            for p in files.values()
        }
        if state.get("assets") and state["assets"] != assets:
            raise PublishError("同版本的正式安装包已变化，请使用新版本号")
        state["assets"] = assets
        save()
        failed = []

        def gitcode():
            command(
                "git",
                "push",
                "gitcode",
                f"{commit}:refs/heads/main",
                f"{tag}:refs/tags/{tag}",
            )
            state["gitcode_source"] = {
                "branch": "main",
                "tag": tag,
                "head": commit,
            }
            save()
            attachments = list(files.values()) + [directory / "assets/SHA256SUMS.txt"]
            if os.environ.get("GITCODE_TOKEN"):
                provider = GitCode(config["gitcode_repo"], os.environ["GITCODE_TOKEN"])
                state["gitcode"] = provider.publish(tag, commit, notes, attachments)
            else:
                from gitcode_browser import GitCodeBrowser

                with LanzouBrowser(config["lanzou"], ROOT) as browser:
                    state["gitcode"] = GitCodeBrowser(
                        browser, config["gitcode_repo"]
                    ).publish(tag, commit, notes, attachments)
            save()

        def lanzou():
            if not config["lanzou"].get("folder_path"):
                raise PublishError("蓝奏云必须指定目标文件夹，不能使用根目录")
            public_url(config["lanzou"].get("folder_share_url", ""))
            copies = directory / "lanzou"
            copies.mkdir(exist_ok=True)
            renamed = {}
            for platform, spec in PACKAGES.items():
                dest = copies / spec[1].format(version=version)
                shutil.copyfile(files[platform], dest)
                verify_file(dest, digest(files[platform]))
                renamed[platform] = dest

            def save_lanzou(results):
                state["lanzou"] = results
                save()

            with LanzouBrowser(config["lanzou"], ROOT) as browser:
                browser.open_folder()
                complete_lanzou(browser, renamed, version, save_lanzou)
            state["lanzou_cleanup_complete"] = True
            save()

        def updater():
            if not os.environ.get("BBDOWN_UPDATE_TOKEN"):
                raise PublishError("更新后台需配置本机 BBDOWN_UPDATE_TOKEN")
            if state.get("stages", {}).get("gitcode", {}).get("status") != "complete":
                raise PublishError("请先完成 GitCode 上传")
            state["updater"] = UpdateServer(
                config["updater"], os.environ["BBDOWN_UPDATE_TOKEN"]
            ).publish(version, notes, files, state["gitcode"])
            save()

        operations = {"gitcode": gitcode, "lanzou": lanzou}
        if updater_requested(args.stage, config):
            operations["updater"] = updater
        for name, operation in operations.items():
            if args.stage not in ("all", name):
                continue
            if not run_stage(name, operation, state, save, retry=args.stage != "all"):
                failed.append(name)
        lines = [f"BBDown Pro {version}", "", notes, ""]
        folder_url = config["lanzou"].get("folder_share_url")
        if folder_url:
            lines.append("下载文件夹：" + folder_url)
            if config["lanzou"].get("folder_password"):
                lines.append("提取码：" + config["lanzou"]["folder_password"])
        for item in state.get("lanzou", {}).values():
            lines.append(item["name"] + "：" + item["url"])
        (directory / "用户下载说明.txt").write_text("\n".join(lines) + "\n")
        if failed:
            raise PublishError(
                "未完成：" + "、".join(failed) + "；记录已保存，用 --resume 补做"
            )
        print(f"本次步骤完成，记录和下载说明：{directory}")


if __name__ == "__main__":
    sys.modules.setdefault("publish_release", sys.modules[__name__])
    try:
        main()
    except (PublishError, requests.RequestException) as error:
        # Do not print request exceptions containing tokens / pre-signed URLs.
        message = (
            str(error)
            if isinstance(error, PublishError)
            else type(error).__name__ + "（网络请求失败，未完成步骤可重试）"
        )
        print("发布停止：" + message, file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("发布已停止，已完成的记录保留，可加 --resume 继续。", file=sys.stderr)
        sys.exit(130)
    except Exception as error:  # noqa: BLE001 - CLI boundary hides token-bearing library exceptions.
        print(
            f"发布停止：{type(error).__name__}，请检查本地截图或登录状态；已完成步骤保留。",
            file=sys.stderr,
        )
        sys.exit(1)
