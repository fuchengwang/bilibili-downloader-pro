# 一次命令发布新版

入口为 `bash scripts/release.sh`，`bash scripts/publish.sh` 是同一个入口的别名。正式安装包由现有 GitHub Actions 构建；脚本等待两平台正式 Release 就绪，下载并校验 Windows ZIP/macOS DMG，同步 GitCode 源码、标签与安装包，再分发到固定蓝奏云文件夹。需要本机安装 Git、GitHub CLI (`gh`)、uv 和 Google Chrome，并在脚本等待和分发期间保持电脑运行。

## 首次配置

配置文件为 `.local/publish.json`，不会进入 Git。可从示例复制：

```sh
mkdir -p .local
cp scripts/publish.example.json .local/publish.json
chmod 600 .local/publish.json
```

蓝奏云目标固定为 `根目录 / 闲鱼商品 / B站下载器_BBDown`。分享地址为 `https://wwbcj.lanzouu.com/b0he8ozcb`，提取码 `1234`。脚本不会创建、删除文件夹，也不会修改分享地址、提取码或文件夹描述。

更新后台默认不启用，不影响两个下载渠道的发布。要同时操作后台，设 `updater.enabled=true`，填写 `updater.base_url`（更新后台的 HTTPS 服务地址），并配置 `BBDOWN_UPDATE_TOKEN`（管理员 JWT）。`updater.publish=true` 表示全部试下载通过后正式发布；设为 `false` 可只准备通过验证的草稿。JWT 过期后需重新取得。

GitHub 使用 `gh auth login` 的登录。GitCode 源码历史、main 分支与本次标签同步使用已配置的 `gitcode` SSH remote；Release 附件使用独立 Chrome 的网页登录。SSH 密钥不能代替附件上传的网页登录。如已配置 `GITCODE_TOKEN`，可选用官方 API 发布 GitCode 附件；蓝奏云始终通过网页操作。环境变量请配置到本机受保护的环境中，勿放入源码、命令历史或提交到 Git。

首次登录两个网页账号：

```sh
bash scripts/release.sh --login-lanzou
bash scripts/release.sh --login-gitcode
```

脚本打开独立 Chrome 窗口，请正常登录。无需回终端按键，脚本会自动确认登录并保存，最长等待十分钟。蓝奏云会确认目标文件夹可打开；GitCode 会确认目标仓库的发行版编辑页面可打开。确认前请保持窗口打开。浏览器状态保存在本机，日常 Chrome 配置独立保留。登录过期时重新执行相应命令。浏览器目录位于 `.local/publish/lanzou-profile`，应当与登录凭据一样保密。

日常上传、下载和删除默认使用无头 Chrome，直接操作网页控件，不调用蓝奏云 API。公开下载沿固定文件夹输入密码、点击文件名、点击普通下载的正常路径，保留网页导航与会话；不会直接拼接网盘下载地址。若页面仍要求人工验证，停止并保留旧版，可加 `--visible` 重试，在可见 Chrome 中完成验证。

## 发布与重试

修改 `wails.json` 的 `info.productVersion` 为新的 `X.Y.Z`，提交该版本的代码，然后执行：

```sh
bash scripts/release.sh
```

脚本执行顺序：

1. 推送代码与版本标签，触发原有正式构建。构建未成功、安装包未公开就绪时，不进行分发。
2. 下载 Windows ZIP、签名公证后的 macOS DMG 和 SHA256SUMS.txt，核对文件完整性。
3. 通过 SSH 推送 GitCode main 分支、源码提交历史和本次标签，然后用已登录网页创建 Release、上传附件，以匿名完整下载校验附件；已有同名附件也必须重新校验，不覆盖不同内容。正常推送不使用 force 或 mirror。
4. 在本地复制安装包并改名为 `Windows版 BBDown ProX.Y.Z.zip` 和 `MacOS版 BBDown ProX.Y.Z.dmg`，上传同一个蓝奏云文件夹。改名不改变安装包内容。
5. 在无网盘账号登录的浏览器环境中，从固定分享文件夹取得两个文件，完整下载并核对大小、SHA-256。两个平台均通过后，删除该文件夹中名称严格匹配、版本更低的 BBDown 安装包。相同版本、新版本和其他文件均保留；不会操作回收站中的文件。
6. 如启用更新后台，将 GitCode 作为自动更新源，填入后台版本、说明、两个平台、文件大小和校验值。调用现有试下载接口，补查未通过项；所有地址通过后再正式发布。蓝奏云分享页不填入自动更新直链。

构建未成功时不分发安装包。分发中的网络故障、账号过期、页面布局变化或校验失败会记录在对应渠道；GitCode 和蓝奏云分别尝试，已完成的内容保留。修复原因后继续：

```sh
bash scripts/release.sh --resume
```

只补做一个渠道也可使用：

```sh
bash scripts/release.sh --resume --stage gitcode
bash scripts/release.sh --resume --stage lanzou
bash scripts/release.sh --resume --stage updater
```

`--resume` 使用同一版本已发布的正式包，跳过记录中完成的渠道，补做未完成的渠道；明确指定 `--stage` 会重新检查该渠道。同名文件会再次校验，已完成的后台发布也会核对安装包。若同一个版本已有不同的安装包或已撤回，脚本停止，不自动覆盖或重新发布。需要重建已有标签时用 `gh workflow run release.yml -f tag=vX.Y.Z`；`--resume` 只补做分发。

每个版本的本地记录在 `.local/publish/vX.Y.Z/`：`state.json` 保存步骤状态、校验值与失败原因，`用户下载说明.txt` 包含更新说明和固定文件夹链接，可直接发给用户。整个流程使用本机文件锁，避免两个发布脚本同时上传、删除。浏览器出错时保留本地截图 `lanzou-last-error.png` 便于检查当前页面；该截图不自动上传。

## 只读检查

显示计划，不上传或删除：

```sh
bash scripts/release.sh --plan
```

只检查当前版本的固定蓝奏云分享入口，匿名下载并与 GitHub 正式包对照，不上传或删除：

```sh
bash scripts/release.sh --verify-lanzou
```

## 验证与限制

```sh
uv run --with requests==2.32.5 --with playwright==1.58.0 python -m unittest discover -s scripts/tests -v
bash -n scripts/publish.sh scripts/release.sh
```

自动化使用真实网页控件；蓝奏云的上传、文件行、删除按钮和确认弹窗定位可在 `lanzou` 配置中调整。若未找到唯一控件，该渠道停止操作。当前真实账号中已验证蓝奏云测试文件上传与删除，现有 1.1.9 的两个安装包也已通过无头 Chrome 匿名完整下载校验。GitCode 已实测临时预发布版本的创建、两个安装包和校验文件的真实上传、匿名完整下载校验，以及再次执行时复用同名附件；临时发行版和远端、本地测试标签均已清理，正式版本与附件保留。后续如出现人工验证或页面变化，脚本保留旧版并记录失败，不能保证第三方网页永久无需人工操作。

请使用本机发布入口来发布。直接推送标签只会触发 GitHub 构建，浏览器分发需要这个本机脚本继续运行；不会在 GitHub 托管 runner 中复用本机网页登录状态。

当前脚本运行于 macOS/Linux，使用 Chrome 与本机文件锁。凭据不在发布记录中保存。这个脚本不会实现“立即推送”或改变客户端检查周期，后台正式发布后客户端继续按既有策略发现更新。
