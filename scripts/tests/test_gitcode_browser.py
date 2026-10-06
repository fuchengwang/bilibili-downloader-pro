import hashlib
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.parse import unquote, urlparse

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from gitcode_browser import GitCodeBrowser
from lanzou_browser import LanzouBrowser
from publish_release import PublishError


class GitCodeFlowTests(unittest.TestCase):
    def test_create_verify_resume_and_reject_changed_existing_asset(self):
        """Exercise real Chrome controls against intercepted local fixture pages."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo, tag = "fixture/repo", "v1.1.10"
            base = "https://gitcode.com/" + repo
            events, pending, published = [], {}, {}
            files = []
            for name in ["windows.zip", "macos.dmg", "SHA256SUMS.txt"]:
                path = root / name
                path.write_bytes(name.encode())
                files.append(path)

            def route(request):
                url = urlparse(request.request.url)
                body = ""
                if url.path.endswith("/upload"):
                    value = request.request.post_data_json
                    pending[value["name"]] = bytes(value["bytes"])
                    events.append(("upload", value["name"]))
                    request.fulfill(json={"success": True})
                    return
                if url.path.endswith("/publish"):
                    published.update(pending)
                    events.append(("publish", tag))
                    request.fulfill(json={"success": True})
                    return
                if url.path.endswith("/releases"):
                    body = "<a>发行版</a>" + "".join(
                        f'<a href="{base}/releases/download/{tag}/{name}">{name}</a>'
                        for name in published
                    )
                elif url.path.endswith("/releases/create"):
                    body = f"""
                    <div><label>Tag名称</label><input id="tag"/><button id="choice">{tag}</button></div>
                    <div><label>发行版标题</label><input/></div>
                    <div><label>发行版描述</label><textarea></textarea></div>
                    <input type="file" multiple id="d-upload-temp"/><div id="files"></div>
                    <button id="publish" disabled>发布</button>
                    <script>
                    document.querySelector('#choice').onclick=()=>{{document.querySelector('#tag').value='{tag}';document.querySelector('#choice').remove();}};
                    document.querySelector('#d-upload-temp').onchange=async(e)=>{{
                      for(const f of e.target.files){{
                        const row=document.createElement('div');row.textContent=f.name;document.querySelector('#files').append(row);
                        await fetch('{base}/upload',{{method:'POST',headers:{{'Content-Type':'application/json'}},body:JSON.stringify({{name:f.name,bytes:Array.from(new Uint8Array(await f.arrayBuffer()))}})}});
                      }}document.querySelector('#publish').disabled=false;
                    }};
                    document.querySelector('#publish').onclick=async()=>{{await fetch('{base}/publish',{{method:'POST'}});location.href='{base}/releases';}};
                    </script>
                    """
                else:
                    self.fail("Unexpected browser route: " + url.path)
                request.fulfill(body=body, content_type="text/html; charset=utf-8")

            def verify(url, expected, size):
                name = unquote(urlparse(url).path.rsplit("/", 1)[1])
                content = published[name]
                events.append(("verify", name))
                if (
                    len(content) != size
                    or hashlib.sha256(content).hexdigest() != expected
                ):
                    raise PublishError("文件不一致")

            with (
                LanzouBrowser(
                    {"profile": str(root / "profile"), "headless": True}, root
                ) as browser,
                patch("gitcode_browser.verify_url", side_effect=verify),
            ):
                browser.context.route("**/*", route)
                provider = GitCodeBrowser(browser, repo)
                provider.login()
                self.assertTrue((browser.profile / "gitcode-login-ready").is_file())
                self.assertFalse(events)
                result = provider.publish(tag, "commit", "release notes", files)
                self.assertEqual(set(result), {path.name for path in files})
                self.assertEqual(
                    [e[0] for e in events],
                    ["upload"] * 3 + ["publish"] + ["verify"] * 3,
                )
                events.clear()
                provider.publish(tag, "commit", "release notes", files)
                self.assertEqual([e[0] for e in events], ["verify"] * 3)
                events.clear()
                published[files[0].name] = b"corrupt"
                with self.assertRaises(PublishError):
                    provider.publish(tag, "commit", "release notes", files)
                self.assertFalse(any(e[0] in ("upload", "publish") for e in events))


if __name__ == "__main__":
    unittest.main()
