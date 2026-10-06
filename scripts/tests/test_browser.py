import sys
import tempfile
import unittest
from pathlib import Path
from urllib.parse import quote, unquote, urlparse

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from lanzou_browser import LanzouBrowser
from publish_release import complete_lanzou


class BrowserFlowTests(unittest.TestCase):
    def test_upload_download_verify_then_delete_through_real_browser_controls(self):
        """Local intercepted pages; never authenticate or mutate the real account."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            files = {
                "Windows版 BBDown Pro1.1.9.zip": b"old-win",
                "MacOS版 BBDown Pro1.1.9.dmg": b"old-mac",
                "用户说明.txt": b"keep",
            }
            events = []
            settings = {
                "profile": str(root / "profile"),
                "headless": True,
                "folder_path": ["闲鱼商品", "B站下载器_BBDown"],
                "folder_share_url": "https://fixture.test/folder",
                "folder_password": "1234",
            }

            def route(request):
                url = urlparse(request.request.url)
                body = ""
                if url.path == "/upload":
                    value = request.request.post_data_json
                    files[value["name"]] = bytes(value["bytes"])
                    events.append(("upload", value["name"]))
                    request.fulfill(json={"success": True})
                    return
                if url.path == "/delete":
                    name = request.request.post_data_json["name"]
                    files.pop(name)
                    events.append(("delete", name))
                    request.fulfill(json={"success": True})
                    return
                if url.path == "/folder":
                    body = "<title>B站下载器_BBDown</title>" + "".join(
                        f'<p><a href="https://fixture.test/file/{quote(name)}">{name}</a></p>'
                        for name in files
                    )
                elif url.path.startswith("/file/"):
                    name = unquote(url.path.removeprefix("/file/"))
                    body = f'<iframe src="https://fixture.test/frame/{quote(name)}"></iframe>'
                elif url.path.startswith("/frame/"):
                    name = unquote(url.path.removeprefix("/frame/"))
                    body = f'<div class="load"><a href="https://fixture.test/download/{quote(name)}">普通下载</a></div>'
                elif url.path.startswith("/download/"):
                    name = unquote(url.path.removeprefix("/download/"))
                    events.append(("download", name))
                    request.fulfill(
                        body=files[name],
                        content_type="application/octet-stream",
                        headers={
                            "Content-Disposition": "attachment; filename=package.bin"
                        },
                    )
                    return
                elif url.path == "/mydisk.php":
                    if url.query == "folder=1":
                        body = '<a href="?folder=2">B站下载器_BBDown</a>'
                    elif url.query == "folder=2":
                        rows = "".join(
                            f'<tr><td><span class="f_name_title">{name}</span></td><td><button class="delete" data-name="{name}">删除</button></td></tr>'
                            for name in files
                        )
                        body = (
                            "<p>闲鱼商品</p><p>B站下载器_BBDown</p><table>"
                            + rows
                            + "</table>"
                        )
                    else:
                        body = '<a href="?folder=1">闲鱼商品</a>'
                    body += """<button id="upload">上传文件</button>
                    <script>
                    document.querySelector('#upload').onclick = () => {
                      const input=document.createElement('input');input.type='file';document.body.append(input);
                      input.onchange=async()=>{const f=input.files[0];await fetch('/upload',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:f.name,bytes:Array.from(new Uint8Array(await f.arrayBuffer()))})});};
                    };
                    document.querySelectorAll('.delete').forEach(b=>b.onclick=async()=>{
                      if(confirm('删除该文件？')){await fetch('/delete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:b.dataset.name})});b.closest('tr').remove();}
                    });
                    </script>"""
                else:
                    self.fail("Unexpected browser route: " + url.path)
                request.fulfill(body=body, content_type="text/html; charset=utf-8")

            packages = {}
            for platform, prefix, extension in [
                ("windows", "Windows", "zip"),
                ("darwin", "MacOS", "dmg"),
            ]:
                path = root / f"{prefix}版 BBDown Pro1.1.10.{extension}"
                path.write_bytes(platform.encode())
                packages[platform] = path
            with LanzouBrowser(settings, root) as browser:
                browser.context.route("**/*", route)
                browser.public.route("**/*", route)
                browser.login()
                self.assertTrue((browser.profile / "login-ready").is_file())
                complete_lanzou(browser, packages, "1.1.10", lambda _: None)
            self.assertIn("用户说明.txt", files)
            self.assertNotIn("Windows版 BBDown Pro1.1.9.zip", files)
            self.assertNotIn("MacOS版 BBDown Pro1.1.9.dmg", files)
            kinds = [event[0] for event in events]
            self.assertEqual(
                kinds, ["upload", "upload", "download", "download", "delete", "delete"]
            )


if __name__ == "__main__":
    unittest.main()
