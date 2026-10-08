# BBDown 发布器

独立的 macOS 应用，通过现有发布脚本发布 Windows 和 macOS 安装包。打开应用不会发布；只有点击「开始发布」才会推送代码、创建版本标签并分发安装包。

## 日常使用

1. 打开 `BBDown 发布器.app`。首次使用进入「设置与登录」，确认 BBDown Pro 项目目录。GitHub、GitCode、蓝奏云登录可以复用当前电脑已有的配置；登录失效时点击对应登录按钮，在独立 Chrome 窗口完成登录。
2. 需要云端更新联动时，在「激活码系统 · 云端更新」输入后台用户名和密码，点击「登录并启用」。默认后台是 `https://47.97.111.181:8090`。登录凭据存入 macOS 系统钥匙串，后台密码不保存。
3. 开发完成后，更新项目 `wails.json` 的 `info.productVersion` 并提交改动。点击「重新检查」，填写可选的更新说明，再点击「开始发布」。留空时沿用 GitHub 自动生成的说明。
4. 查看每一步的状态和运行日志。「打开」按钮可以查看 GitHub Actions / Release、GitCode、蓝奏云、云端后台以及本地记录。
5. 失败或中断后处理提示的问题，再点击「继续发布」。已完成的上传步骤会复用；正式安装包会再次校验。本地任务停止后，已提交到 GitHub 的远程构建仍继续运行。

窗口关闭时，如果任务仍在运行，应用会隐藏窗口并继续任务。再次从 Dock 打开即可查看。使用「停止本机任务」可以取消本地等待、上传或登录；退出整个应用后，下次打开可以继续任务。

## 发布过程

发布前检查 → 推送固定提交和标签 → GitHub 双平台构建、macOS 签名公证 → 下载与 SHA-256 校验 → GitCode 同步代码和安装包、独立下载校验 → 蓝奏云上传和匿名校验 → 云端登记、服务器试下载、正式发布。

云端联动启用后，发布器登记 GitCode 下载链接、文件大小和 SHA-256；服务器试下载通过前不会发布云端版本。蓝奏云的旧版清理仍受「两个新版包全部上传并匿名校验通过」约束，删除进入蓝奏云回收站。

GitCode / 蓝奏云可能要求人工登录或验证码。工具显示 Chrome 窗口供用户完成；页面变化或登录失效会显示错误并保存进度，处理后重试。

## 任务记录与恢复

- 应用设置：`~/Library/Application Support/bbdown-publisher/settings.json`。
- 每版独立记录：项目的 `.local/publish/v版本/publisher-job.json`，包含固定提交、说明、步骤状态和最多 500 条日志。
- 固定发布配置：同目录的 `publisher-config.json`；继续任务时保持 GitCode / 蓝奏云目标，避免改动配置后分发到另一个位置。
- 原脚本记录：同目录的 `state.json`，仍兼容 `bash scripts/release.sh 版本 --resume`。
- 安装包、校验清单、下载说明保存在该版本目录。「本地记录」可直接打开。

这些本地文件使用私有权限，Git 忽略整个 `.local` 目录。云端 JWT 从钥匙串读取后仅传给子进程，不写入配置和日志。

任务固定到版本和提交。继续任务可以在项目已经开发到新版本时进行；GitHub 远程标签必须仍指向原提交，GitCode 同步同一个提交。程序退出码为零、但步骤没有确认完成时，界面仍显示失败。

## 构建和验证

需要 Go、Wails CLI、pnpm；日常使用另需 `uv`、`gh`、Git、Google Chrome。发布器界面直接嵌入应用，不需要本机 Web 服务或单独启动前端。

```bash
bash scripts/build-releaser.sh
go test -race ./tools/releaser/...
uv run --with requests==2.32.5 --with playwright==1.58.0 python -m unittest discover -s scripts/tests -v
```

应用输出为 `tools/releaser/build/bin/BBDown 发布器.app`，同时包含 Apple Silicon 和 Intel 架构。此版本发布器在 Mac 上运行，发布的 BBDown 安装包包含 Windows 和 macOS 两个平台。

自动化测试使用本地临时 Git 仓库、受控子进程、HTTPS 登录服务和浏览器页面替身，覆盖中断恢复、固定提交、停止、错误状态、密码清空、下载校验及旧版清理边界。正式生产发布需要真实账号连接；测试不会创建生产标签或上传生产安装包。
