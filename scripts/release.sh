#!/usr/bin/env bash
# Releases are built and published by GitHub Actions; credentials live in GitHub Secrets.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${1:-$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json)}"
TAG="v${VERSION#v}"
if [[ -n "$(git status --porcelain)" ]]; then
  echo "Commit the release changes before publishing." >&2
  exit 1
fi
if git rev-parse --verify "refs/tags/$TAG" >/dev/null 2>&1; then
  echo "Tag $TAG already exists. Use gh workflow run release.yml -f tag=$TAG to retry." >&2
  exit 1
fi
git push origin HEAD
git tag -a "$TAG" -m "BBDown Pro $TAG"
git push origin "$TAG"
echo "Build status: https://github.com/fuchengwang/bilibili-downloader-pro/actions/workflows/release.yml"
