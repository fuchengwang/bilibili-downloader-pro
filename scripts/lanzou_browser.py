"""Lanzou UI automation. Folder/share settings are never changed."""

from __future__ import annotations

import re
import tempfile
import time
from pathlib import Path
from urllib.parse import urlparse

from playwright.sync_api import Error as PlaywrightError
from playwright.sync_api import sync_playwright

# Imported lazily by the orchestrator to allow pure policy tests without Chrome.
from publish_release import PublishError, verify_file


class LanzouBrowser:
    def __init__(self, settings, root):
        self.settings = settings
        self.profile = root / settings.get("profile", ".local/publish/lanzou-profile")
        self.profile.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.profile.chmod(0o700)
        self.playwright = None
        self.context = None
        self.public = None
        self.download_dir = tempfile.TemporaryDirectory(
            prefix="lanzou-downloads-", dir=self.profile.parent
        )

    def __enter__(self):
        self.playwright = sync_playwright().start()
        try:
            self.context = self.playwright.chromium.launch_persistent_context(
                str(self.profile),
                channel="chrome",
                headless=self.settings.get("headless", True),
                accept_downloads=True,
                downloads_path=self.download_dir.name,
                viewport={"width": 1280, "height": 900},
                locale="zh-CN",
                timezone_id="Asia/Shanghai",
            )
            self.page = (
                self.context.pages[0] if self.context.pages else self.context.new_page()
            )
            self.page.set_default_timeout(15000)
            # Empty context: download verification must not depend on owner login.
            self.public = self.context.browser.new_context(
                accept_downloads=True, locale="zh-CN", timezone_id="Asia/Shanghai"
            )
            self.public_page = self.public.new_page()
            self.public_page.set_default_timeout(15000)
            return self
        except Exception:
            self.__exit__(None, None, None)
            raise

    def __exit__(self, kind, value, traceback):
        if kind and self.context:
            try:
                # Local screenshot only; never dump cookies or raw network responses.
                self.page.screenshot(
                    path=str(self.profile.parent / "lanzou-last-error.png"),
                    full_page=True,
                )
            except (PlaywrightError, OSError):
                print("浏览器已关闭，未能保存错误截图。")
        if self.public:
            self.public.close()
        if self.context:
            self.context.close()
        if self.playwright:
            self.playwright.stop()
        self.download_dir.cleanup()

    def locate(self, text):
        matches = []
        for frame in self.page.frames:
            locator = frame.get_by_text(text, exact=True)
            for index in range(locator.count()):
                item = locator.nth(index)
                if item.is_visible():
                    matches.append(item)
        if len(matches) != 1:
            raise PublishError(f"蓝奏云页面未找到唯一目标：{text}；旧版保留")
        return matches[0]

    def login(self):
        self.page.goto(
            "https://pc.woozooo.com/mydisk.php",
            wait_until="domcontentloaded",
            timeout=60000,
        )
        print(
            "请在打开的独立 Chrome 窗口登录蓝奏云，脚本确认目标文件夹后会自动保存。",
            flush=True,
        )
        self.wait_visible("上传文件", 600)
        self.open_folder()
        (self.profile / "login-ready").touch(mode=0o600)
        print("目标文件夹已确认，登录保存在本机独立浏览器目录。")

    def open_folder(self):
        names = self.settings.get("folder_path", [])
        if not names:
            raise PublishError("未指定蓝奏云目标文件夹")
        self.page.goto(
            "https://pc.woozooo.com/mydisk.php",
            wait_until="domcontentloaded",
            timeout=60000,
        )
        self.wait_visible("上传文件", 30)
        for name in names:
            self.wait_visible(name, 30).click()
            self.page.wait_for_timeout(1000)
        # The final breadcrumb must show the entire configured path.
        visible = "\n".join(
            frame.locator("body").inner_text() for frame in self.page.frames
        )
        if not all(name in visible for name in names):
            raise PublishError("蓝奏云目标文件夹路径未确认；停止操作")
        self.open_public_folder()
        public_names = [
            name.strip()
            for name in self.public_page.locator("a").all_inner_texts()
            if re.search(r"\.(?:zip|dmg|txt)$", name.strip())
        ]
        deadline = time.monotonic() + 30
        while not set(public_names).issubset(self.file_names()):
            if time.monotonic() >= deadline:
                raise PublishError("管理页文件列表尚未加载完整，停止上传或删除")
            self.page.wait_for_timeout(500)

    def wait_visible(self, name, timeout):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                return self.locate(name)
            except PublishError:
                self.page.wait_for_timeout(500)
        raise PublishError(
            f"蓝奏云等待超时：{name}；请先运行 --login-lanzou 检查登录或页面布局"
        )

    def file_names(self):
        # Use actual filename text, never displayed rounded sizes as verification.
        pattern = re.compile(
            r"^(?:Windows版|MacOS版) BBDown Pro\d+\.\d+\.\d+\.(?:zip|dmg)$"
        )
        names = []
        for frame in self.page.frames:
            actual = frame.locator(
                self.settings.get("file_name_selector", ".f_name_title")
            )
            matches = actual if actual.count() else frame.get_by_text(pattern)
            names.extend(
                matches.nth(i).inner_text().strip()
                for i in range(matches.count())
                if matches.nth(i).is_visible()
            )
        return names

    def ensure_upload(self, path):
        names = self.file_names()
        if names.count(path.name) > 1:
            raise PublishError(f"蓝奏云存在重复同名包：{path.name}；停止上传和删除")
        if path.name in names:
            print(f"蓝奏云已有 {path.name}，重新下载验证后继续", flush=True)
            return
        print(f"上传蓝奏云：{path.name}", flush=True)
        self.locate("上传文件").click()

        def file_input():
            items = []
            for frame in self.page.frames:
                preferred = frame.locator(
                    self.settings.get(
                        "upload_input_selector", "#filePicker input[type=file]"
                    )
                )
                if preferred.count() == 1:
                    return preferred
                locator = frame.locator('input[type="file"]')
                for index in range(locator.count()):
                    items.append(locator.nth(index))
            return items[0] if len(items) == 1 else None

        deadline = time.monotonic() + 20
        field = None
        while not field and time.monotonic() < deadline:
            field = file_input()
            self.page.wait_for_timeout(300)
        if field is None:
            raise PublishError("未找到唯一上传控件；页面可能已变化，旧版保留")
        field.set_input_files(str(path))
        for frame in self.page.frames:
            start = frame.locator(
                self.settings.get("upload_start_selector", ".uploadBtn")
            )
            if start.count() == 1 and start.is_visible():
                start.click()
                break
        # The upload dialog can show the name before any bytes are committed.
        # Wait for it to appear at the fixed PUBLIC folder instead.
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            self.open_public_folder()
            if self.public_page.get_by_text(path.name, exact=True).count() == 1:
                break
            self.page.wait_for_timeout(3000)
        else:
            raise PublishError("新版未出现在固定分享入口，旧版保留")
        # Close a completed upload dialog if the filename is also shown there.
        self.page.wait_for_timeout(1000)
        try:
            close = self.locate("关闭")
            close.click()
        except PublishError:
            pass
        self.open_folder()
        if self.file_names().count(path.name) != 1:
            raise PublishError("上传后文件列表未确认，旧版保留")

    def open_public_folder(self):
        url = self.settings.get("folder_share_url", "")
        parsed = urlparse(url)
        if parsed.scheme != "https" or not parsed.hostname:
            raise PublishError("请填写固定蓝奏云文件夹分享链接")
        self.public_page.goto(url, wait_until="domcontentloaded", timeout=60000)
        password = self.public_page.locator("#pwd")
        if password.count() and password.is_visible():
            password.fill(self.settings.get("folder_password", ""))
            self.public_page.locator("#sub").click()
        self.public_page.wait_for_timeout(1000)
        if self.settings["folder_path"][-1] not in self.public_page.title():
            raise PublishError("分享文件夹名称与管理文件夹不一致；旧版保留")

    def share_url(self, name):
        self.open_public_folder()

        def find():
            links = self.public_page.get_by_text(name, exact=True)
            if links.count() != 1:
                return None
            node = links.first
            return node.evaluate("e => (e.closest('a') || e.querySelector('a'))?.href")

        deadline = time.monotonic() + 30
        url = None
        while not url and time.monotonic() < deadline:
            url = find()
            if not url:
                self.public_page.wait_for_timeout(500)
        if not url or urlparse(url).scheme != "https":
            raise PublishError(f"固定分享入口未找到唯一新版包：{name}；旧版保留")
        return url

    def verify_download(self, url, original):
        from publish_release import digest

        downloads = []
        observers = []

        def observe(page):
            callback = lambda download: downloads.append(download)
            page.on("download", callback)
            observers.append((page, callback))

        self.public.on("page", observe)
        observe(self.public_page)
        page = self.public_page
        try:
            # Follow the same folder -> file -> normal download route as a user.
            # Actual navigation supplies the normal referer and share-page cookies.
            self.open_public_folder()
            links = page.locator("a[href]")
            matches = [
                links.nth(i)
                for i in range(links.count())
                if links.nth(i).evaluate("e => e.href") == url
            ]
            if len(matches) != 1:
                raise PublishError("固定分享入口未找到唯一文件链接，旧版保留")
            matches[0].click()
            page.wait_for_timeout(1500)
            for candidate in self.public.pages:
                if urlparse(candidate.url).path == urlparse(url).path:
                    page = candidate
                    break
            selector = self.settings.get(
                "download_selector", ".load a, #download a, a#download, a[download]"
            )
            deadline = time.monotonic() + 30
            clicked = False
            while not clicked and time.monotonic() < deadline:
                for frame in page.frames:
                    ordinary = frame.get_by_text("普通下载", exact=True)
                    targets = (
                        ordinary if ordinary.count() == 1 else frame.locator(selector)
                    )
                    for index in range(targets.count()):
                        target = targets.nth(index)
                        if target.is_visible():
                            target.click()
                            clicked = True
                            break
                    if clicked:
                        break
                page.wait_for_timeout(300)
            if not clicked:
                raise PublishError(
                    "未找到蓝奏云公开下载按钮；遇到验证码或页面变化，旧版保留"
                )
            deadline = time.monotonic() + 300
            prompted = False
            validation_clicks = {}
            final_clicks = set()
            while not downloads and time.monotonic() < deadline:
                for popup in self.public.pages:
                    if popup.is_closed() or not popup.locator("body").count():
                        continue
                    text = popup.locator("body").inner_text(timeout=3000)
                    immediate = popup.get_by_role("link", name="立即下载", exact=True)
                    if (
                        immediate.count() == 1
                        and immediate.is_visible()
                        and popup not in final_clicks
                    ):
                        immediate.click()
                        final_clicks.add(popup)
                        continue
                    # The centre verification control is the ordinary visible step;
                    # a later "立即下载" link appears only after it completes.
                    for selector in (
                        "#sub > [onclick^='down_r(']",
                        "#go > [onclick^='down_r(']",
                    ):
                        ordinary_validation = popup.locator(selector)
                        if (
                            popup not in validation_clicks
                            and ordinary_validation.count() == 1
                            and ordinary_validation.is_visible()
                        ):
                            ordinary_validation.click()
                            validation_clicks[popup] = time.monotonic()
                            break
                    if "需要验证" in text or "验证码" in text:
                        if time.monotonic() - validation_clicks.get(popup, 0) < 10:
                            continue
                        if self.settings.get("headless", True):
                            popup.screenshot(
                                path=str(
                                    self.profile.parent
                                    / "lanzou-download-verification.png"
                                )
                            )
                            raise PublishError(
                                "蓝奏云下载要求网页验证；请加 --visible 完成验证，旧版保留"
                            )
                        if not prompted:
                            print(
                                "蓝奏云下载要求验证，请在打开的窗口完成验证；完成后脚本会继续校验。",
                                flush=True,
                            )
                            prompted = True
                page.wait_for_timeout(500)
            if len(downloads) != 1:
                raise PublishError("未取得唯一下载文件，旧版保留")
            # Poll Chrome's completed file, rather than blocking indefinitely in
            # Download.save_as() if the server starts a download then stalls.
            completed = None
            while time.monotonic() < deadline:
                files = [
                    file
                    for file in Path(self.download_dir.name).iterdir()
                    if file.is_file()
                    and not file.name.endswith((".crdownload", ".tmp"))
                ]
                if len(files) == 1:
                    completed = files[0]
                    break
                page.wait_for_timeout(500)
            if completed is None:
                downloads[0].cancel()
                raise PublishError("蓝奏云完整下载超时，旧版保留")
            try:
                if completed.stat().st_size != original.stat().st_size:
                    raise PublishError("蓝奏云下载文件大小不一致，旧版保留")
                verify_file(completed, digest(original))
            finally:
                completed.unlink(missing_ok=True)
            print(f"蓝奏云匿名下载校验通过：{original.name}", flush=True)
        finally:
            self.public.remove_listener("page", observe)
            for observed, callback in observers:
                observed.remove_listener("download", callback)
            for other in self.public.pages[:]:
                if other != self.public_page:
                    other.close()

    def delete(self, name):
        # Called only behind the two-platform download verification barrier.
        self.open_folder()
        if name not in self.file_names():
            return
        node = self.locate(name)
        rows = self.settings.get("row_selector", "tr, .f_tb, .file-row")
        handle = node.evaluate_handle("(e, selector) => e.closest(selector)", rows)
        row = handle.as_element()
        if row is None:
            raise PublishError("无法确认旧版文件所在行；停止删除")
        buttons = row.query_selector_all(
            self.settings.get(
                "delete_selector",
                ".f_selb, [onclick*='del'], [title*='删除'], [aria-label*='删除'], .delete",
            )
        )
        buttons = [button for button in buttons if button.is_visible()]
        if len(buttons) != 1:
            raise PublishError("未找到唯一旧版删除按钮；停止删除")

        # Accept only the dialog raised by this exact file's delete button.
        def confirm_delete(dialog):
            if dialog.type == "confirm":
                dialog.accept()
            else:
                dialog.dismiss()

        self.page.once("dialog", confirm_delete)
        try:
            buttons[0].click()
        finally:
            self.page.remove_listener("dialog", confirm_delete)
        self.page.wait_for_timeout(1000)
        # HTML confirmation dialogs require an explicitly configured selector.
        confirm = self.settings.get("delete_confirm_selector", "[onclick^='f_dec(']")
        if confirm:
            for frame in self.page.frames:
                locator = frame.locator(confirm)
                if locator.count() == 1 and locator.is_visible():
                    locator.click()
                    break
        self.open_folder()
        if name in self.file_names():
            raise PublishError("旧版删除未确认，记录保留，重新运行可继续")
