#!/usr/bin/env bash
# ==============================================================================
# BBDown Pro 一键跨平台打包与发布脚本 (macOS DMG + Windows ZIP)
# 包含：原生签名、Hardened Runtime、精美 DMG 封装、Windows 交叉编译、GitHub Release 发布
# 用法:
#   ./scripts/release.sh         # 自动从 wails.json 读取版本发布
#   ./scripts/release.sh 1.1.0   # 指定版本号发布
# ==============================================================================
set -euo pipefail

if [ -f "$PWD/wails.json" ]; then
  ROOT_DIR="$PWD"
else
  TARGET_FILE="${BASH_SOURCE[0]}"
  while [ -L "$TARGET_FILE" ]; do
    TARGET_DIR="$(cd -P "$(dirname "$TARGET_FILE")" && pwd)"
    TARGET_FILE="$(readlink "$TARGET_FILE")"
    [[ $TARGET_FILE != /* ]] && TARGET_FILE="$TARGET_DIR/$TARGET_FILE"
  done
  ROOT_DIR="$(cd -P "$(dirname "$TARGET_FILE")/.." && pwd)"
fi
cd "$ROOT_DIR"

export PATH="$PATH:$(go env GOPATH)/bin:$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin"

# 1. 确定发布版本号
VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  VERSION=$(grep '"productVersion"' wails.json | sed -E 's/.*"productVersion": *"([^"]+)".*/\1/')
fi
VERSION="${VERSION#v}"
TAG="v${VERSION}"

echo "========================================================"
echo " BBDown Pro 跨平台发布流程 -> ${TAG}"
echo "========================================================"

# 2. 检查基础工具链
for TOOL in go pnpm wails create-dmg gh security zip; do
  if ! command -v "$TOOL" >/dev/null 2>&1; then
    echo "缺少必要工具: $TOOL" >&2
    exit 1
  fi
done

# 3. 查找 Apple Developer ID 签名证书
SIGN_IDENTITY=$(security find-identity -v -p codesigning | grep "Developer ID Application" | head -n 1 | sed -E 's/.*"([^"]+)".*/\1/' || true)
if [ -n "$SIGN_IDENTITY" ]; then
  echo "==> 匹配到 Apple 开发者签名证书: ${SIGN_IDENTITY}"
else
  echo "[警告] 未在 Keychain 中发现 Developer ID Application 证书，将使用临时签名"
  SIGN_IDENTITY="-"
fi

# 4. 执行快速单元测试与前端构建
echo "==> [1/6] 运行单元测试..."
go test -short ./...

echo "==> [2/6] 构建前端项目..."
(cd frontend && pnpm install --frozen-lockfile && pnpm run build)

# 5. 构建与签名 macOS Universal App
echo "==> [3/6] 构建 macOS Universal 应用程序..."
rm -rf "build/bin/BBDown Pro.app" "build/bin"/*.dmg "build/bin"/*.zip
wails build -platform darwin/universal

APP_PATH="build/bin/BBDown Pro.app"
if [ ! -d "$APP_PATH" ]; then
  echo "未找到构建产物: $APP_PATH" >&2
  exit 1
fi

echo "==> 执行 macOS 应用签名 (Hardened Runtime + Timestamp)..."
xattr -cr "$APP_PATH"
if [ "$SIGN_IDENTITY" != "-" ]; then
  codesign --force --deep --options runtime --timestamp --sign "$SIGN_IDENTITY" "$APP_PATH"
else
  codesign --force --deep --sign - "$APP_PATH"
fi
codesign --verify --deep --strict "$APP_PATH"

# 6. 使用 create-dmg 制作 DMG 安装包
DMG_PATH="build/bin/BBDown-Pro-macOS-universal.dmg"
echo "==> [4/6] 封装 macOS DMG 安装包..."
rm -f "$DMG_PATH"
create-dmg \
  --volname "BBDown Pro" \
  --window-pos 200 120 \
  --window-size 600 400 \
  --icon-size 100 \
  --icon "BBDown Pro.app" 150 190 \
  --hide-extension "BBDown Pro.app" \
  --app-drop-link 450 190 \
  "$DMG_PATH" \
  "$APP_PATH" || true

if [ ! -f "$DMG_PATH" ]; then
  echo "DMG 生成失败: $DMG_PATH" >&2
  exit 1
fi

if [ "$SIGN_IDENTITY" != "-" ]; then
  echo "==> 对 DMG 镜像文件进行签名..."
  codesign --force --sign "$SIGN_IDENTITY" "$DMG_PATH"
fi

# 7. 构建与打包 Windows x64 版本
echo "==> [5/6] 交叉编译 Windows x64 版本..."
wails build -platform windows/amd64

WIN_EXE="build/bin/BBDown Pro.exe"
WIN_ZIP="build/bin/BBDown-Pro-Windows-amd64.zip"
if [ ! -f "$WIN_EXE" ]; then
  echo "未找到 Windows 可执行文件: $WIN_EXE" >&2
  exit 1
fi

echo "==> 压缩 Windows 发布包..."
(cd build/bin && rm -f "$WIN_ZIP" && zip -9 "BBDown-Pro-Windows-amd64.zip" "BBDown Pro.exe")

# 8. 发布 / 更新到 GitHub Releases
echo "==> [6/6] 上传发布至 GitHub Releases (${TAG})..."

if ! git rev-parse "$TAG" >/dev/null 2>&1; then
  git tag "$TAG"
fi
git push origin "$TAG" || true

RELEASE_TITLE="BBDown Pro ${TAG} - 下载核心稳定性重构与跨平台发布"
RELEASE_NOTES="### BBDown Pro ${TAG} 更新说明

#### 核心稳定性与架构重构
- **并发与死锁治理**：彻底消除全部暂停/继续时的锁重入死锁，通知回调均在互斥锁外安全派发；
- **Worker 隔离与取消安全**：引入代次 Token 机制，杜绝快速暂停/恢复时的双 Worker 竞态与取消句柄误删；
- **下载断点与 Range 严格校验**：严格校验 HTTP 206 Content-Range 起点，416 时自动清理超长损坏文件并从头重下；
- **多候选 CDN 自动容灾**：完整保留骨干与备用 CDN 列表，在网络断流、403、5xx 异常时自动轮换重试；
- **原生 MP4 事务性合并**：合成先写临时文件，完整封装校验成功后原子替换，防止破坏已有同名成品；
- **任务状态线程安全与原子持久化**：采用深拷贝快照与原子重命名写入，杜绝数据竞争与异常崩溃损坏文件。

#### 体验与系统交互优化
- **原生打开目录/定位文件**：采用标准 Win32 ShellExecute 与原生 Explorer 调用，彻底消除 Windows 下控制台黑框闪烁；
- **全新 3D 品牌图标**：更新透明底 3D 拟物小电视图标，覆盖 Windows 图标、任务栏、应用内侧边栏 Logo 及 Favicon；
- **前后端事件完整对齐**：补齐任务状态更新与完成事件广播，下载完成提示与彩带动效实时触发。

#### 发布安装包说明
- \`BBDown-Pro-macOS-universal.dmg\`：macOS 原生 DMG 安装包（支持拖拽安装至 Applications，已注入 Developer ID 签名，支持 Apple Silicon 与 Intel 架构）。
- \`BBDown-Pro-Windows-amd64.zip\`：Windows x64 便携压缩包（解压即用）。"

if gh release view "$TAG" >/dev/null 2>&1; then
  echo "==> 更新已有 Release 资产..."
  gh release upload "$TAG" "$DMG_PATH" "$WIN_ZIP" --clobber
  gh release edit "$TAG" --title "$RELEASE_TITLE" --notes "$RELEASE_NOTES"
else
  echo "==> 创建全新 Release..."
  gh release create "$TAG" "$DMG_PATH" "$WIN_ZIP" --title "$RELEASE_TITLE" --notes "$RELEASE_NOTES"
fi

echo ""
echo "========================================================"
echo " 发布成功！"
echo "  - Release 页面: https://github.com/fuchengwang/bilibili-downloader-pro/releases/tag/${TAG}"
echo "  - macOS 产物: ${DMG_PATH}"
echo "  - Windows 产物: ${WIN_ZIP}"
echo "========================================================"
