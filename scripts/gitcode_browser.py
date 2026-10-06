"""GitCode Release publishing through the signed-in Chrome page."""

from __future__ import annotations

import re
from urllib.parse import quote

from playwright.sync_api import TimeoutError as BrowserTimeout
from publish_release import PublishError, digest, verify_url, wait_until


class GitCodeBrowser:
    def __init__(self, browser, repo):
        if not re.fullmatch(r"[\w.-]+/[\w.-]+", repo):
            raise PublishError("GitCode 仓库格式应为 owner/repo")
        self.browser = browser
        self.page = browser.page
        self.base = "https://gitcode.com/" + repo

    def open_releases(self):
        self.page.goto(
            self.base + "/releases", wait_until="domcontentloaded", timeout=60000
        )
        self.page.get_by_text("发行版", exact=True).first.wait_for(timeout=30000)
        self.page.wait_for_timeout(1500)

    def login(self):
        self.page.goto(
            self.base + "/releases/create", wait_until="domcontentloaded", timeout=60000
        )
        print(
            "请在打开的 Chrome 窗口登录 GitCode；出现发行版编辑页面后，脚本会自动保存登录。",
            flush=True,
        )
        try:
            self.page.get_by_text("发行版标题", exact=True).wait_for(timeout=600000)
        except BrowserTimeout as error:
            raise PublishError(
                "GitCode 登录等待超时，请重新运行 --login-gitcode"
            ) from error
        if not self.page.url.startswith(self.base + "/releases/"):
            raise PublishError("GitCode 发布仓库未确认，登录未保存")
        (self.browser.profile / "gitcode-login-ready").touch(mode=0o600)
        print("GitCode 网页发布权限已确认，登录保存在本机。", flush=True)

    def asset_links(self, tag):
        prefix = self.base + "/releases/download/" + quote(tag, safe="") + "/"
        links = self.page.locator("a[href]").evaluate_all(
            "nodes => nodes.map(n => ({name:n.textContent.trim(),url:n.href}))"
        )
        result = {}
        for item in links:
            if item["url"].startswith(prefix):
                if item["name"] in result:
                    raise PublishError("GitCode 页面存在重复同名附件，停止发布")
                result[item["name"]] = item["url"]
        return result

    def field(self, label, selector):
        labels = self.page.get_by_text(label, exact=True).filter(visible=True)
        if labels.count() != 1:
            raise PublishError(f"GitCode 未找到唯一表单标签：{label}")
        ancestor = labels.first
        for _ in range(5):
            ancestor = ancestor.locator("xpath=..")
            fields = ancestor.locator(selector).filter(visible=True)
            if fields.count() == 1:
                return fields.first
            if fields.count() > 1:
                break
        raise PublishError(f"GitCode 表单控件发生变化：{label}，停止发布")

    def select_tag(self, tag):
        field = self.field("Tag名称", "input")
        field.click()
        if field.is_editable():
            field.fill(tag)
        choice = self.page.get_by_text(tag, exact=True).filter(visible=True)
        try:
            choice.first.wait_for(timeout=15000)
        except BrowserTimeout as error:
            raise PublishError(
                "GitCode 未找到已推送的版本标签，不自动创建其他标签"
            ) from error
        if choice.count() != 1:
            raise PublishError("GitCode 版本标签选择不唯一，停止发布")
        choice.click()
        if field.input_value() != tag:
            raise PublishError("GitCode 版本标签未确认，停止发布")

    def publish(self, tag, commit, notes, files):
        self.open_releases()
        links = self.asset_links(tag)
        results = {}
        # Reuse an existing release only after every existing asset matches bytes.
        for path in files:
            if path.name in links:
                verify_url(links[path.name], digest(path), path.stat().st_size)
                results[path.name] = links[path.name]
        missing = [path for path in files if path.name not in links]
        if not missing:
            print("GitCode 现有附件全部下载校验通过。", flush=True)
            return results
        if self.page.get_by_role("button", name="登录", exact=True).count():
            raise PublishError(
                "GitCode 发布附件需先运行 bash scripts/release.sh --login-gitcode 登录网页"
            )
        existing = self.page.get_by_text("BBDown Pro " + tag, exact=True).filter(
            visible=True
        )
        if links or existing.count():
            self.page.goto(
                self.base + "/releases/" + quote(tag, safe=""),
                wait_until="domcontentloaded",
                timeout=60000,
            )
            self.page.wait_for_timeout(2000)
            edit = self.page.get_by_role("button", name="编辑", exact=True)
            if edit.count() != 1:
                edit = self.page.get_by_text("编辑", exact=True).filter(visible=True)
            if edit.count() != 1:
                raise PublishError(
                    "GitCode 已有版本缺少附件，未找到唯一编辑入口；请检查本地页面截图"
                )
            edit.click()
            self.page.get_by_text("发行版标题", exact=True).wait_for(timeout=30000)
        else:
            self.page.goto(
                self.base + "/releases/create",
                wait_until="domcontentloaded",
                timeout=60000,
            )
            self.page.get_by_text("发行版标题", exact=True).wait_for(timeout=30000)
            self.select_tag(tag)
        self.field("发行版标题", "input").fill("BBDown Pro " + tag)
        editor = self.field("发行版描述", "textarea, [contenteditable=true]")
        editor.fill(notes or "BBDown Pro " + tag)
        upload = self.page.locator("#d-upload-temp")
        if upload.count() != 1:
            upload = self.page.locator("input[type=file]")
        if upload.count() != 1:
            raise PublishError("GitCode 未找到唯一附件上传控件，停止发布")
        upload.set_input_files([str(path) for path in missing])
        for path in missing:
            self.page.get_by_text(path.name, exact=True).first.wait_for(timeout=30000)
        publish = self.page.get_by_role("button", name="发布", exact=True)
        if publish.count() != 1:
            raise PublishError("GitCode 未找到唯一发布按钮，停止发布")

        def uploaded():
            percentages = self.page.get_by_text(re.compile(r"^\d+%$")).filter(
                visible=True
            )
            if any(value.strip() != "100%" for value in percentages.all_inner_texts()):
                return False
            return publish.is_enabled()

        wait_until(uploaded, 300, "GitCode 附件上传完成")
        publish.click()

        # Only the public download links are used as evidence of success.
        def ready():
            self.open_releases()
            current = self.asset_links(tag)
            return current if all(path.name in current for path in files) else None

        links = wait_until(ready, 120, "GitCode 已发布附件", interval=5)
        for path in files:
            verify_url(links[path.name], digest(path), path.stat().st_size)
            results[path.name] = links[path.name]
            print("GitCode 匿名下载校验通过：" + path.name, flush=True)
        return results
