# README

## About

This is the official Wails Vue-TS template.

You can configure the project by editing `wails.json`. More information about the project settings can be found
here: https://wails.io/docs/reference/project-config

## Live Development

To run in live development mode, run `wails dev` in the project directory. This will run a Vite development
server that will provide very fast hot reload of your frontend changes. If you want to develop in a browser
and have access to your Go methods, there is also a dev server that runs on http://localhost:34115. Connect
to this in your browser, and you can call your Go code from devtools.

## Building

To build a redistributable, production mode package, use `wails build`.

## 专业版激活

应用已接入自建激活服务，使用独立 AppID `bbdown-pro`。首次使用需要联网输入激活码；激活后授权凭证绑定本机并加密保存，可断网启动和下载。激活后的前 15 天每天最多静默核对一次，网络临时不可用不会阻止正常使用。

换机时可在“偏好设置”底部解绑本机，再在新电脑上输入原激活码；旧电脑无法操作时，由卖家在激活后台解绑。下载解析、添加任务和恢复任务均在 Go 层检查授权，激活页面不是唯一限制入口。

## 自动构建与发布

提交修改后运行 `bash scripts/release.sh`（版本取自 `wails.json`），或推送新的 `v*` 标签。GitHub Actions 会运行后端测试，分别构建 Windows x64 ZIP 与 macOS Universal DMG；两个平台全部成功后才公开 Release，并附上 SHA256SUMS.txt。

macOS 构建必须配置仓库 Actions Secrets：

- `APPLE_CERTIFICATE_P12`：包含 Developer ID Application 证书及私钥的 P12 文件，Base64 编码。
- `APPLE_CERTIFICATE_PASSWORD`：P12 导出密码。
- `APPLE_ID`：Apple 开发者账户邮箱。
- `APPLE_APP_SPECIFIC_PASSWORD`：该账户生成的应用专用密码。
- `APPLE_TEAM_ID`：证书对应的开发团队 ID。

流水线使用临时钥匙串导入证书，对 Universal App 启用 Hardened Runtime 和时间戳，提交 Apple 公证并装订 App 票据，再生成包含 Applications 快捷方式的 DMG，对 DMG 再签名、公证、装订并执行 Gatekeeper 验证。缺少凭据或验证失败会阻止公开发布。首次打开仍可能显示 macOS 正常的“从互联网下载”确认。

本机如需验证同样的打包过程，先运行 `wails build -platform darwin/universal`，配置 `APPLE_SIGN_IDENTITY` 和 `APPLE_NOTARY_PROFILE`（或上述 Apple 账户的三个环境变量），再运行 `bash scripts/package-macos.sh`。仅代码签名、不公证的产物不会被该脚本接受。

## 外观与下载验证

“偏好设置 → 外观”支持浅色、深色和跟随系统，修改立即生效并自动保存；其他未保存的表单修改不会随主题切换一起保存。

下载器会检查番剧播放接口的错误码和试看标记。仅返回试看流时，任务会报告权限不足，不会把试看内容当作完整视频保存。会员登录凭据正常传递给播放接口，实际可下载画质以服务端返回的媒体流为准。

解析单集链接后，结果卡显示该集的封面、标题和时长，并同时提供“下载这一集”和“选择分集”。整季或课程首页以选集为入口；多P链接未指定分P时明确显示“默认P1”。合集序号与稿件内分P序号分别保存，打开B站或下载时始终使用实际目标。

选集窗口支持按标题关键词和集号即时筛选（如“美学”“P8”“第8集”）。全选和反选仅作用于当前结果，切换关键词会保留已选项；底部说明不在当前结果中的已选数量，可通过“查看已选”复核。集号区间选择与编码设置位于“更多选项”。

普通视频使用当前账号的B站播放器权限标记识别充电专属内容。明确无完整播放权限时，解析结果和失败任务显示充电原因及登录/视频页入口；已授权内容继续正常获取媒体。未确认原因的无媒体结果不会被推断为未充电。试看、登录、大会员、购买、地区及网络问题分别提示，技术信息可展开查看。

课堂课程支持 `https://www.bilibili.com/cheese/play/ep课时编号` 和 `.../ss课程编号`，也可从剪贴板自动识别。解析会获取完整课时目录，支持单课时或批量下载。免费课和当前账号已购买且未过期的正式课均通过课堂播放接口获取媒体；课程购买权限独立于大会员，付费课须登录购买该课程的账号。试看和权限不足会明确报错。

受保护的课堂媒体按官方播放器的授权流程处理后，再由内置 Go 合并器生成 MP4。播放授权参数只保留在当前任务内存中，不保存到任务记录；暂停或处理失败时保留原始下载缓存，恢复任务后重新确认账号播放权限。

可复现的验证命令：

- `pnpm --dir frontend run build`
- `go test -race -short ./...`：包括播放接口兼容、权限/试看处理、断点下载、音频缺失与分片偏移回归测试。
- `pnpm --dir frontend exec node --test tests/quality-loading.test.mjs tests/episode-selection.test.mjs tests/clipboard.test.mjs tests/update-panel.test.mjs`：验证当前集画质、权限刷新、搜索与选择范围及剪贴板自动解析。
- `BBDOWN_CHARGE_DENIED_BVID=未充电视频BV号 BBDOWN_CHARGE_GRANTED_BVID=已充电视频BV号 go test ./pkg/bilibili -run '^TestLiveChargeAccessComparison$' -count=1 -v`：只读对照验证当前保存账号的充电权限与完整媒体地址，不创建下载任务或输出账号凭据。
- `BBDOWN_CHEESE_TEST_EPISODES=课时编号列表 go test ./pkg/downloader -run '^TestLiveCheeseDownload$' -v`：用当前登录账号实际下载选定课堂课时（编号用逗号分隔），缓存和成品与正常下载记录隔离。
- `BBDOWN_MEDIA_TESTS=1 go test ./pkg/downloader -run TestGeneratedMediaMerge -v`：需要本机 FFmpeg 和 FFprobe，生成 AVC/HEVC/AV1 + AAC/FLAC/E-AC-3 样本，检查合并后的双轨道、时长及完整解码。应用本身的合并流程仍使用纯 Go。

v1.1.5 发布前已在线下载并合并普通视频与《工作细胞》公开第 1 集，完整解码通过；无会员权限的第 2 集被正确识别为试看。没有可用的大会员账号，因此会员完整内容的实际下载尚未实测，已用接口样例验证会话传递与高清流选择。

## 软件更新

偏好设置新增“软件更新”卡片，默认开启“自动检查更新”。自动发现新版只显示设置图标蓝点，用户点击后下载，可查看进度、暂停和继续。下载完成可立即重启或留到下次启动安装。包完整校验与安装恢复由 Go 层负责，不改动已有授权及用户数据。发布包要求、模块分工与验证命令见 [更新模块说明](docs/updater.md)。
