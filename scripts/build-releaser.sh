#!/usr/bin/env bash
set -euo pipefail
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WAILS_BIN="$(command -v wails || true)"
if [[ -z "$WAILS_BIN" && -x "$HOME/Tools/go/bin/wails" ]]; then
  WAILS_BIN="$HOME/Tools/go/bin/wails"
fi
if [[ -z "$WAILS_BIN" ]]; then
  echo '请先安装 Wails CLI：go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0' >&2
  exit 1
fi
cd "$PROJECT_ROOT/tools/releaser"
"$WAILS_BIN" build -platform darwin/universal -ldflags "-X 'main.defaultProject=$PROJECT_ROOT'"
echo "发布器：$PROJECT_ROOT/tools/releaser/build/bin/BBDown 发布器.app"
