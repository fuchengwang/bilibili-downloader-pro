import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import publish_release as release


class FakeBrowser:
    def __init__(self, failure=None):
        self.events = []
        self.failure = failure
        self.names = [
            "Windows版 BBDown Pro1.1.9.zip",
            "MacOS版 BBDown Pro1.1.9.dmg",
            "Windows版 BBDown Pro1.2.0.zip",
            "用户说明.txt",
            "其他工具1.1.0.zip",
        ]

    def ensure_upload(self, path):
        self.events.append(("upload", path.name))
        if self.failure == ("upload", path.name):
            raise release.PublishError("上传失败")
        if path.name not in self.names:
            self.names.append(path.name)

    def share_url(self, name):
        return "https://example.com/" + name

    def verify_download(self, url, path):
        self.events.append(("verify", path.name))
        if self.failure == ("verify", path.name):
            raise release.PublishError("文件不一致")

    def file_names(self):
        return self.names

    def delete(self, name):
        self.events.append(("delete", name))
        self.names.remove(name)


class PublishingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.files = {}
        for platform, spec in release.PACKAGES.items():
            path = Path(self.temp.name) / spec[1].format(version="1.1.10")
            path.write_bytes(platform.encode())
            self.files[platform] = path

    def test_both_uploaded_and_verified_before_deletion(self):
        browser = FakeBrowser()
        saved = []
        result = release.complete_lanzou(
            browser, self.files, "1.1.10", lambda r: saved.append(dict(r))
        )
        self.assertEqual(
            [e[0] for e in browser.events],
            ["upload", "upload", "verify", "verify", "delete", "delete"],
        )
        self.assertEqual(set(result), {"windows", "darwin"})
        self.assertEqual(len(saved), 2)
        self.assertIn("Windows版 BBDown Pro1.2.0.zip", browser.names)
        self.assertIn("用户说明.txt", browser.names)
        self.assertIn("其他工具1.1.0.zip", browser.names)

    def test_any_upload_or_verification_failure_preserves_all_old_packages(self):
        for action in ["upload", "verify"]:
            for path in self.files.values():
                with self.subTest(action=action, package=path.name):
                    browser = FakeBrowser((action, path.name))
                    with self.assertRaises(release.PublishError):
                        release.complete_lanzou(
                            browser, self.files, "1.1.10", lambda _: None
                        )
                    self.assertFalse(any(e[0] == "delete" for e in browser.events))
                    self.assertIn("Windows版 BBDown Pro1.1.9.zip", browser.names)
                    self.assertIn("MacOS版 BBDown Pro1.1.9.dmg", browser.names)

    def test_resume_after_partial_cleanup_verifies_again(self):
        browser = FakeBrowser()
        browser.names.remove("Windows版 BBDown Pro1.1.9.zip")
        release.complete_lanzou(browser, self.files, "1.1.10", lambda _: None)
        browser.events = []
        release.complete_lanzou(browser, self.files, "1.1.10", lambda _: None)
        self.assertEqual(
            [e[0] for e in browser.events], ["upload", "upload", "verify", "verify"]
        )

    def test_one_platform_is_not_enough_to_delete(self):
        browser = FakeBrowser()
        with self.assertRaises(release.PublishError):
            release.complete_lanzou(
                browser, {"windows": self.files["windows"]}, "1.1.10", lambda _: None
            )
        self.assertFalse(any(e[0] == "delete" for e in browser.events))

    def test_versions_are_numeric_and_name_matching_is_strict(self):
        names = [
            "Windows版 BBDown Pro1.1.9.zip",
            "MacOS版 BBDown Pro1.1.9.dmg",
            "Windows版 BBDown Pro1.1.10.zip",
            "Windows版 BBDown Pro1.1.11.zip",
            "MacOS版 BBDown Pro1.1.9.zip",
            "Windows版 BBDown Pro1.1.9.zip.bak",
            "Windows版 BBDown Pro1.1.9.zip\n",
            "Windows版 其他软件1.1.9.zip",
        ]
        self.assertEqual(release.older_packages(names, "1.1.10"), sorted(names[:2]))

    def test_altered_package_is_rejected(self):
        path = self.files["windows"]
        checksum = release.digest(path)
        path.write_bytes(b"corrupt")
        with self.assertRaises(release.PublishError):
            release.verify_file(path, checksum)

    def test_state_is_private_and_atomic(self):
        dest = Path(self.temp.name) / "state.json"
        release.json_write(dest, {"version": "1.1.10"})
        self.assertEqual(json.loads(dest.read_text()), {"version": "1.1.10"})
        self.assertEqual(dest.stat().st_mode & 0o777, 0o600)
        self.assertFalse(dest.with_suffix(".tmp").exists())

    def test_failed_channel_does_not_block_other_channel_or_expose_credentials(self):
        state, saved = {}, []

        def failed():
            raise RuntimeError("secret signed URL must not be recorded")

        def other():
            state["lanzou"] = "verified"

        save = lambda: saved.append(json.loads(json.dumps(state)))
        self.assertFalse(release.run_stage("gitcode", failed, state, save))
        self.assertTrue(release.run_stage("lanzou", other, state, save))
        self.assertEqual(state["stages"]["gitcode"]["status"], "failed")
        self.assertEqual(state["stages"]["lanzou"]["status"], "complete")
        self.assertNotIn("secret", json.dumps(saved))

    def test_resume_reuses_completed_channel_but_explicit_stage_rechecks(self):
        state = {"stages": {"gitcode": {"status": "complete"}}}
        operation = unittest.mock.Mock()
        release.run_stage("gitcode", operation, state, lambda: None)
        operation.assert_not_called()
        release.run_stage("gitcode", operation, state, lambda: None, retry=True)
        operation.assert_called_once()

    def test_update_backend_is_optional_and_can_be_requested_explicitly(self):
        config = {"updater": {"enabled": False}}
        self.assertFalse(release.updater_requested("all", config))
        self.assertTrue(release.updater_requested("updater", config))
        self.assertTrue(
            release.updater_requested("all", {"updater": {"enabled": True}})
        )

    def test_existing_tag_never_triggers_a_second_build(self):
        calls = []

        def command(*args):
            calls.append(args)
            return "v1.1.10" if args[:2] == ("git", "tag") else ""

        with (
            patch.object(release, "command", side_effect=command),
            self.assertRaisesRegex(release.PublishError, "--resume"),
        ):
            release.trigger_build("1.1.10")
        self.assertFalse(any(args[:2] == ("git", "push") for args in calls))

    def test_failed_formal_build_stops_before_asset_download(self):
        calls = []

        def command(*args):
            calls.append(args)
            if args[:2] == ("git", "rev-parse"):
                return "commit"
            if args[:3] == ("gh", "release", "view"):
                raise release.PublishError("尚未发布")
            if args[:3] == ("gh", "run", "list"):
                return json.dumps(
                    [
                        {
                            "headSha": "commit",
                            "headBranch": "v1.1.10",
                            "status": "completed",
                            "conclusion": "failure",
                        }
                    ]
                )
            return ""

        with (
            patch.object(release, "command", side_effect=command),
            self.assertRaisesRegex(release.PublishError, "正式构建未成功"),
        ):
            release.prepare_assets("fixture/repo", "v1.1.10", Path(self.temp.name), 1)
        self.assertFalse(
            any(args[:3] == ("gh", "release", "download") for args in calls)
        )

    def test_http_200_business_error_is_rejected(self):
        server = release.UpdateServer({"base_url": "https://example.com"}, "test-token")
        with patch.object(release.requests, "request") as request:
            request.return_value.json.return_value = {
                "success": False,
                "message": "未通过检查",
            }
            with self.assertRaisesRegex(release.PublishError, "未通过检查"):
                server.api("POST", "/releases/1/publish")

    def test_backend_reuses_passed_checks_and_publishes_last(self):
        server = release.UpdateServer({"base_url": "https://example.com"}, "test-token")
        calls = []

        def api(method, path, **kwargs):
            calls.append((method, path, kwargs))
            if path == "/releases" and method == "GET":
                return {"list": [], "total": 0}
            if path == "/releases" and method == "POST":
                return {"id": 2, "status": "draft", "check_status": "pending"}
            if path == "/releases/2/test-download":
                self.assertEqual(kwargs["json"], {"force": False})
                return {"id": 2}
            if path == "/releases/2":
                return {"id": 2, "status": "draft", "check_status": "passed"}
            if path == "/releases/2/publish":
                return {"id": 2, "status": "published"}
            self.fail(path)

        with patch.object(server, "api", side_effect=api):
            result = server.publish(
                "1.1.10",
                "notes",
                self.files,
                {
                    s[0]: "https://example.com/" + s[0]
                    for s in release.PACKAGES.values()
                },
            )
        self.assertEqual(result["status"], "published")
        self.assertEqual(calls[-1][1], "/releases/2/publish")
        artifacts = calls[1][2]["json"]["artifacts"]
        self.assertTrue(
            all(a["size"] > 0 and len(a["sha256"]) == 64 for a in artifacts)
        )
        self.assertTrue(all(a["sources"][0]["name"] == "GitCode" for a in artifacts))

    def test_failed_backend_download_is_never_published(self):
        server = release.UpdateServer({"base_url": "https://example.com"}, "test-token")
        calls = []

        def api(method, path, **kwargs):
            calls.append(path)
            if path == "/releases" and method == "GET":
                return {"list": [], "total": 0}
            return {"id": 2, "status": "draft", "check_status": "failed"}

        with (
            patch.object(server, "api", side_effect=api),
            self.assertRaises(release.PublishError),
        ):
            server.publish(
                "1.1.10",
                "",
                self.files,
                {
                    s[0]: "https://example.com/" + s[0]
                    for s in release.PACKAGES.values()
                },
            )
        self.assertNotIn("/releases/2/publish", calls)


if __name__ == "__main__":
    unittest.main()
