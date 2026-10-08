"""Real Chromium interactions with a local publisher bridge fixture; no accounts."""
import copy
import functools
import http.server
import json
import threading
import unittest
from pathlib import Path

from playwright.sync_api import sync_playwright

UI = Path(__file__).resolve().parents[2] / "tools/releaser/frontend"


class PublisherUITests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        class QuietHandler(http.server.SimpleHTTPRequestHandler):
            def log_message(self, *args):
                pass
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(QuietHandler, directory=str(UI)))
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def test_drafts_survive_polling_start_uses_bridge_and_failed_login_clears_password(self):
        state = {
            "settings": {"project":"/fixture/project","cloudURL":"https://example.com","cloudEnabled":False,"visibleBrowser":True},
            "version":"1.1.12","commit":"a"*40,"checks":[{"id":"github","name":"GitHub 已登录","ok":True}],
            "history":[],"job":None,"busy":False,"activity":"","notice":"","cloudLoggedIn":False,
        }
        calls = []
        def bridge(source,method,args):
            calls.append((method,args))
            if method == "LoginCloud":
                raise RuntimeError("后台登录未成功，请检查用户名和密码")
            if method == "SaveSettings":
                state["settings"] = args[0]
            if method == "Start":
                self.assertEqual(args,["本次修复更新重启",False])
                state["job"] = {"project":"/fixture/project","version":"1.1.12","commit":"a"*40,"notes":args[0],"status":"running","error":"","started":"2026-10-08T00:00:00Z","updated":"2026-10-08T00:00:00Z","steps":[{"id":"build","name":"双平台构建","status":"running","message":"等待签名公证"}],"logs":[],"cloudEnabled":False}
                state["busy"]=True
                state["activity"]="release"
            if method == "Stop":
                state["busy"]=False
                state["activity"]=""
                state["job"]["status"]="stopped"
            return copy.deepcopy(state)

        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(channel="chrome",headless=True)
            page = browser.new_page(viewport={"width":1180,"height":850})
            page.expose_binding("publisherFixture",bridge)
            page.add_init_script("""window.go={main:{App:new Proxy({}, {get:(_,method)=>(...args)=>window.publisherFixture(method,args)})}};""")
            page.goto(f"http://127.0.0.1:{self.server.server_port}")
            page.get_by_role("button",name="开始发布 v1.1.12",exact=True).wait_for()
            self.assertFalse(any(c[0]=="Start" for c in calls),"Opening the tool must not publish")
            page.get_by_label("更新说明").fill("本次修复更新重启")
            page.wait_for_timeout(1200)
            self.assertEqual(page.get_by_label("更新说明").input_value(),"本次修复更新重启")
            page.get_by_role("button",name="设置与登录").click()
            page.get_by_label("后台用户名",exact=True).fill("fixture-admin")
            page.get_by_label("后台密码",exact=True).fill("private-fixture-password")
            page.get_by_role("button",name="登录并启用").click()
            page.get_by_text("后台登录未成功，请检查用户名和密码",exact=False).first.wait_for()
            self.assertEqual(page.get_by_label("后台密码",exact=True).input_value(),"")
            page.get_by_role("button",name="关闭设置",exact=True).click()
            page.get_by_role("button",name="开始发布 v1.1.12",exact=True).click()
            page.get_by_text("等待签名公证",exact=True).wait_for()
            page.get_by_role("button",name="停止本机任务",exact=True).click()
            page.get_by_role("button",name="继续发布 v1.1.12",exact=True).wait_for()
            self.assertEqual(sum(c[0]=="Start" for c in calls),1)
            browser.close()


if __name__ == "__main__":
    unittest.main()
