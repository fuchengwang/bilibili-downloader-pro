# Ghost Downloader 4 原理参考与 Go 下载内核

本分支从 `main` 的 `ec04e28` 开始，替换媒体传输实现。`codex/download-diagnostics` 的未提交实验保留在原目录。本分支的前端页面、布局、Wails 方法和任务 JSON 字段保持原有接口。

## 参考来源

核对的官方源码版本：`XiaoYouChR/Ghost-Downloader-3`，提交 `256b082166fec444b517dd3f4bb88c55ead1325b`。官方仓库沿用旧名称，当前产品为 4.x。

| 官方源码 | 借鉴的机制 | 本项目实现 |
| --- | --- | --- |
| [HTTP 功能包](https://github.com/XiaoYouChR/Ghost-Downloader-3/blob/256b082166fec444b517dd3f4bb88c55ead1325b/features/http_pack/task.py) | 成功响应后拆分、空闲 worker 接管最大剩余范围、位置写入、直接接手右侧未收到数据的范围、范围恢复记录 | `range_engine.go`、`range_record.go` |
| [全局速度计](https://github.com/XiaoYouChR/Ghost-Downloader-3/blob/256b082166fec444b517dd3f4bb88c55ead1325b/app/services/speed_meter.py) | 所有任务共享速率预算，缓存恢复字节不算网络速度 | 保留并接入 Go 共享令牌桶 `bandwidth.go`、`progress.go` |
| [任务服务](https://github.com/XiaoYouChR/Ghost-Downloader-3/blob/256b082166fec444b517dd3f4bb88c55ead1325b/app/services/task_service.py) | 等待队列与运行集合分开、并发名额统一管理 | 保留主分支持久化 FIFO 和 worker token，增加队列共享的网络连接预算 |
| [Bilibili 取流](https://github.com/XiaoYouChR/Ghost-Downloader-3/blob/256b082166fec444b517dd3f4bb88c55ead1325b/features/bili_pack/stream.py)、[下载步骤](https://github.com/XiaoYouChR/Ghost-Downloader-3/blob/256b082166fec444b517dd3f4bb88c55ead1325b/features/bili_pack/task.py) | 取流、视频、音频、合并分层；Range 探测使用原始字节编码 | 现有 Bilibili 权限和媒体选择层 + 新传输层 + 原有 Go 合并器 |

代码为本项目的 Go 实现，没有引入 Ghost Python 源文件或运行时依赖。参考项目的许可证见其 [LICENSE](https://github.com/XiaoYouChR/Ghost-Downloader-3/blob/256b082166fec444b517dd3f4bb88c55ead1325b/LICENSE)。

## 传输与调度

- 视频、音频同时开始，各自使用现有设置中的 1–8 个初始 worker。成功验证第一个响应前，每个轨道只发起一个请求；小于 64 KiB 的尾块不再拆分。
- 按 Ghost 的自动加速原理，连续 5 个一秒样本在均值的 15% 内时增加 4 个 worker，再观察 5 秒；速度增长不足并发增长比例的 80% 时停止扩容。每轨最多 16 个 worker，全队列最多持有 24 个媒体响应；开启全局限速时停止自动扩容。
- 同一流的 worker 直接对一个媒体文件 `WriteAt`。边界调整、所有权转移和写入由同一把锁保护。完成自己的范围后，可拆分最大剩余尾部；右侧尚未收到数据时，撤销其 worker 并沿用当前连接继续读取。
- 网络 `Read` 进行中发生拆分时，写入严格截到最新边界。如果已读取的数据越过移交边界，关闭该响应，避免网络游标与写入游标错位。
- 请求和实际响应读取均以 2 MiB 为上限，按已写入位置继续下一段。即使 CDN 忽略请求结束位置，也不会让一次请求无限延长。分段请求使用新连接，整体连接预算仍统一管理，等待可以取消。被接管的 worker 会重新领取剩余范围，避免永久退出后并发收缩。
- 现有全局限速实时生效，覆盖全部任务、视频、音频和范围。限速等待、磁盘写入和排队不计入网络空闲超时。
- Bilibili 任务使用接口提供的码率与时长估算初始大小，并在原有大小文本中标注“约”，随后直接从实际下载响应确认两个轨道的长度；不再先等待微小 Range 探测，也不再等待视频下载完才获取音频长度。估算值不能用于文件完整性或完成判定。独立流接口的可选探测仍有总计 8 秒、单源 2 秒的预算。
- 候选 CDN 保留播放接口返回的主源与备用顺序，不再凭主机名称强制优先大陆节点，不构造未返回的替代域名。主源短暂故障的冷却结束后，可以重新优先使用主源。

## 恢复与完整性

下载中的媒体直接放在用户选择的输出目录：视频为 `标题 [画质].mp4.downloading`，音频为 `标题 [画质].audio.downloading`。每条轨道只写一个文件，与线程数量无关；Bilibili DASH 通常有视频和音频两个轨道。正式 `.mp4` 只在合并成功后发布，下载期间不创建空的成品占位文件。合并成功后沿用“删除临时文件”设置清理这些轨道。

桌面应用把每条轨道的断点放在应用数据目录的 `download-checkpoints` 下，按轨道路径的哈希索引。断点包含版本、总长度、ETag/Last-Modified、媒体与校验器标识，以及连续覆盖整个文件的 `[start, position, end)` 范围。下载目录里只创建视频、音频两条媒体文件。没有有效断点时，预分配文件的逻辑长度不计为下载进度。

旧版在轨道旁写入 `.ranges.json` 和 `.ranges`。任务继续时会兼容读取带点和不带点的旧记录，先保存到应用数据目录，再清理旧旁文件。

写入进度在媒体文件 `Sync` 后原子保存，每秒一次，暂停和退出时再保存一次。完成后仍保留完整记录，合并成功且开启清理时随缓存删除。进程意外中止后，最后一次检查点之后的数据可能重下，已经落盘的范围不会被跳过。记录中不保存 Cookie 或签名直链。

恢复时验证范围连续性、边界、物理文件和服务端长度；同源已有验证器发生变化时重新下载。不同 CDN 的 ETag/Last-Modified 不能混用为 If-Range，切换时继续验证实际长度。记录中的源标识只保存 origin/path 的 SHA256，不保存直链。`206` 严格检查 Content-Range 起点、结束、总长以及 Content-Length。已经证实支持 Range 后，偶发 `200` 会拒绝写入并有界重试，不再清空所有 worker 的有效进度；起始阶段反复证实不支持 Range 时才回退整流。未知总长度的 EOF 不会报告完成。磁盘错误直接终止并保留可恢复状态。

旧版连续文件前缀可以续传；旧版 `.part-*` 没有持久化分片布局，无法证明其边界时重新传输，在新下载成功后清理旧分片。任务历史、队列位置和主动暂停状态继续沿用。新记录支持修改并发数后续传。

已有任务的系统缓存会在用户继续该任务、最终输出文件名确定后迁回输出目录。迁移逐字节保留媒体、范围记录和旧分片，先复制并落盘，再原子保存任务的新路径，最后删除旧缓存。复制、取消或保存失败时保留旧路径和原数据，仅回收本次创建的副本；目标同名下载中文件属于其他任务时不会覆盖。应用启动时不搬动大文件，也不扫描普通下载目录。

## 重试与换链

同一流的 worker 共享候选源和冷却时间；`429/503` 遵守有界 Retry-After。`401/403/410` 触发合并后的换链请求，每秒最多刷新一次。一次任务的视频和音频共用取流刷新器，固定最初选择的画质和编码，防止刷新时拼接不同媒体表示。

无进度的连续失败最多 8 次；只有实际写入新数据才重置无进度计数，签名 URL 变化本身不会重置。读停滞会保留已经收到的范围并从候选源继续；额外并发被拒绝时，健康连接可以接手尚未收到数据的右侧范围。

实际联网验证中观察到元数据 GET 也可能在响应前返回 EOF，因此只读 API 请求增加最多 3 次的网络错误/截断/429/5xx 重试，总预算 30 秒。登录 POST、403/412 和 JSON 中的权限错误不重放。

2026-11-04 修复了实际桌面环境遗漏的连接路径故障：进程继承本机 HTTP 代理，媒体 CDN 请求在代理路径上持续于响应前返回 EOF，即使更换 CDN 主机仍然失败。媒体 GET 现在先沿用原代理路径；仅在 EOF、连接重置/拒绝或网络超时、且请求尚未取消时，保持相同 Range/If-Range 尝试直连。直连获得 200/206 后按主机及代理组合优先使用一分钟；直连连接失败可回退代理。两条路径共享各自连接池和全局连接预算，不改系统代理、API 请求或 TLS 验证，不对 HTTP 权限错误、证书错误或其他网站启用该回退。

同一视频 `BV1ZL4c6NEmM` 的六个视频/音频候选地址做了对照：代理路径 12/12 次 EOF，直连 12/12 次 206 且完整读取请求的 256 KiB。修复后保留继承代理环境，以完整 DownloadManager 下载 1080P60 AVC，39,614,163 字节在 15.46 秒内下载合并完成，FFmpeg 全片解码通过。记录见 `build/qa/reported-eof-20261104/run-01/acceptance.json`。此前主动清除代理的吞吐测试未覆盖这一桌面启动条件；该结果仅证明此次复现和修复，不代表所有网络路径下的长期稳定性。

第二次沿用用户的“最高画质 / 自动编码”设置，保留代理环境并断言实际发生路径回退，13.26 秒完成同样的 39,614,163 字节，全片解码通过，成品 SHA256 与第一次一致；记录见 `build/qa/reported-eof-20261104/run-02/acceptance.json`。重新构建并打开桌面应用后，直接重试原来失败的任务，保留其已有缓存，由应用正常完成下载并移入完成历史；原任务成品的独立全片解码也通过，见 `build/qa/reported-eof-20261104/native-retry.json`。整个过程没有修改系统代理或界面。

本机打开验证还发现旧版缓存整理会无条件枚举下载目标目录，系统 Downloads 目录的打开操作阻塞时，整个授权后初始化也会等待。本次取消与历史缓存无关的启动扫描，将缓存迁移推迟到任务继续时。使用 FIFO 模拟阻塞目录的回归测试保证空队列及普通下载目录内存在暂停任务时，启动都不会枚举目标目录；前端和原有窗口显示流程没有改动。“打开文件夹”优先定位该任务的视频 `.downloading` 文件，避免选中音频或进度旁文件。

本次传输协议覆盖 Bilibili 的 HTTP 媒体下载和测量驱动的自动扩容。Ghost 的 QUIC、浏览器 TLS 指纹和其他协议功能包未移植；本次同源对照未显示必须引入 TLS 指纹才能达到参考吞吐的证据。

## 验证命令

```sh
go test -race -short ./... -count=1 -timeout 180s
go test -race -short ./pkg/downloader -run 'TestRangeEngine|TestRepeatedPauseResume' -count=10 -timeout 180s
go test -race ./pkg/downloader -run '^TestBandwidthSlowSharedStreamsDoNotTriggerNetworkIdleTimeout$' -count=1
go vet ./...
pnpm --dir frontend exec node --test 'tests/*.test.mjs'
pnpm --dir frontend run build
wails build -platform darwin/arm64 -s -skipbindings -m -nosyncgomod
```

真实 CDN 验证采用隔离目录，不修改用户队列，并使用当前 Bilibili 会话。选择一个可正常播放的公开视频：

```sh
BBDOWN_GHOST_LIVE_URL='https://www.bilibili.com/video/有效BV号/' \
BBDOWN_GHOST_QA_DIR='/绝对路径/独立验证目录' \
go test -race ./pkg/downloader -run '^TestGhostLiveDownload$' -count=1 -v -timeout 6m
```

该测试强制探测 EOF、两次连接错误和一次响应体截断；下载视频并与独立普通 HTTP GET 的 SHA256 比对；下载音频，经 Go 合并器封装后用 FFprobe 检查轨道和包，再用 FFmpeg 完整解码。成功时输出 MP4 和不含凭据的 `verification.txt`。每次使用新的验证目录，避免已有完整缓存跳过故障注入。

## 2026-11-03 完整性验证与性能纠正

- 后端 `go test -race -short ./...`、`go vet ./...` 通过；动态范围、跨并发数恢复和断流相关测试重复运行通过。
- 前端原有 102 项测试及生产资源构建通过，前端源码没有修改。
- 90 个读取器共享 1 KiB/s 的限速测试通过，没有把限速等待误判为网络停滞。
- 真实公开视频 `BV1xx411c7mD` / CID `62131`，360P AVC：视频 `39,980,845` 字节、音频 `34,610,750` 字节；视频 SHA256 与独立 HTTP GET 一致。
- 早期 Go 合并后的 MP4 包含视频和音频两个轨道，时长 `2055.637732` 秒，FFmpeg 完整解码通过。故障注入覆盖 2 次探测 EOF、2 次连接 EOF、1 次响应体截断，整个验证耗时约 4 分 10 秒。这只能证明该次完整性与恢复，不代表速度达标；用户反馈后增加了以下持续吞吐验收。
- macOS arm64 本地应用已构建、代码签名校验通过，并实际打开原有界面、完成视频解析。构建产物位于 `build/bin/BBDown Pro.app`，真实媒体和验证记录位于 `build/qa/ghost-core-20261103/`。

前四轮完整应用性能测试分别出现低吞吐、错误降级或等待超时，均未作为性能通过。对应修正包括 CDN 顺序、主源恢复优先级、两个轨道同时下载、按源使用验证器、瞬时 200 不清空进度、worker 接管后重新调度和实际读取上限。

同字节参考使用 `scripts/qa/ghost_reference.py`：下载并校验上述固定提交的官方 `HttpTaskStep` 源码，仅适配桌面任务元数据、配置、文件 I/O、固定 DNS 与客户端构造，原版分片、接管和加速逻辑不改动。使用 wreq 0.13.0 / Chrome154 / MacOS，启用证书验证，关闭限速。这是官方 HTTP 步骤的隔离对照，不能表述为整个 Ghost GUI 应用的性能测试。

同一签名视频 URL、同一 Akamai 节点 `116.89.245.168`、同一 `40,030,956` 字节文件，按顺序运行，无同时竞争带宽：

| 客户端 | 初始 worker | 总耗时 | 首字节至末字节平均吞吐 |
| --- | --- | --- | --- |
| Ghost 官方 HTTP 步骤，第 1 次 | 8 | 120.59 秒 | 329.54 KiB/s |
| Go 新内核，第 1 次 | 8 | 29.86 秒 | 1381.09 KiB/s |
| Ghost 官方 HTTP 步骤，第 2 次 | 8 | 102.53 秒 | 386.71 KiB/s |
| Go 新内核，第 2 次 | 4 | 58.85 秒 | 675.81 KiB/s |

四次 SHA256 均为 `a3148ea59be96f3d868e137491fad560c48c8680fe9ec000f0a1b9c86cf554ec`。网络在测量期间存在波动，这些结果支持“本次样本未观察到 Go 额外吞吐劣化”，不能推出普遍快于 Ghost 或任何网络下必定满速。用户已说明当时网络较弱，不采用固定 800 KiB/s 或 1 MB/s 作为通过门槛。

修正后的第 5 轮使用实际 `DownloadManager`、独立状态目录、默认 4 个初始 worker：480P AVC，视频 `40,030,956` 字节，音频 `34,610,750` 字节，总计 `74,641,706` 字节。首个进度样本在 2.14 秒出现，2.89 秒确认两轨真实总长，传输期平均 `728.9 KiB/s`，显示峰值 `2036.6 KiB/s`。总长度与成品一致，Go 合并和 FFmpeg 全文件解码通过。原有用户队列未用于测试，保持暂停。

性能原始记录位于 `build/qa/performance-20261103/{ghost-reference-01,go-reference-01,ghost-reference-02,go-reference-02,manager-run-05}/`，均不包含签名直链或 Cookie。

复现：先用新的隔离目录运行完整管理器验收，它会生成仅本机可读的 `/tmp/bbdown-transport-reference.json`，供同字节对照使用。Shell 带有代理环境变量时，在每个命令前使用 `env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u http_proxy -u https_proxy -u all_proxy` 统一测试路由，不修改系统网络设置。固定节点只作用于测试 Transport；正常应用沿用系统 DNS。

```sh
BBDOWN_PERFORMANCE_ACCEPTANCE=1 BBDOWN_PERFORMANCE_DIR=/绝对路径/新的管理器验收目录 \
go test -race ./pkg/downloader -run '^TestLiveManagerPerformanceAcceptance$' -count=1 -v -timeout 11m

# 可选 BBDOWN_GHOST_HTTP_SOURCE=/绝对路径/固定版本task.py 使用已缓存源码；仍校验哈希。
BBDOWN_REFERENCE_DIR=/绝对路径/新的Ghost对照目录 uv run scripts/qa/ghost_reference.py

BBDOWN_DIRECT_GHOST_REFERENCE=1 BBDOWN_REFERENCE_THREADS=8 BBDOWN_REFERENCE_DIR=/绝对路径/新的Go对照目录 \
go test -race ./pkg/downloader -run '^TestLiveDirectGhostReference$' -count=1 -v -timeout 5m
```

以上是本机与该真实 CDN 样本的验证记录；Windows、QUIC 和长时间跨网络环境运行未在此记录中宣称通过。
