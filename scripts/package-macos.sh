#!/usr/bin/env bash
# Sign, notarize, and package the already-built universal app. Never publish unsigned builds.
set -euo pipefail
cd "$(dirname "$0")/.."
APP_PATH="build/bin/BBDown Pro.app"
DMG_PATH="build/bin/BBDown-Pro-macOS-universal.dmg"
: "${APPLE_SIGN_IDENTITY:?Developer ID Application identity is required}"
if [[ "$APPLE_SIGN_IDENTITY" != "Developer ID Application:"* ]]; then
  echo "A Developer ID Application certificate is required." >&2
  exit 1
fi
if [[ -n "${APPLE_NOTARY_PROFILE:-}" ]]; then
  NOTARY_ARGS=(--keychain-profile "$APPLE_NOTARY_PROFILE")
else
  : "${APPLE_ID:?Apple ID is required}"
  : "${APPLE_APP_SPECIFIC_PASSWORD:?Apple app-specific password is required}"
  : "${APPLE_TEAM_ID:?Apple team ID is required}"
  NOTARY_ARGS=(--apple-id "$APPLE_ID" --password "$APPLE_APP_SPECIFIC_PASSWORD" --team-id "$APPLE_TEAM_ID")
fi
[[ -d "$APP_PATH" ]]
lipo "$APP_PATH/Contents/MacOS/BBDown Pro" -verify_arch arm64 x86_64
xattr -cr "$APP_PATH"
codesign --force --deep --options runtime --timestamp --sign "$APPLE_SIGN_IDENTITY" "$APP_PATH"
codesign --verify --deep --strict --verbose=2 "$APP_PATH"
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT
notarize() {
  local target="$1" result="$2"
  xcrun notarytool submit "$target" "${NOTARY_ARGS[@]}" --wait --timeout 30m --output-format json > "$result"
  if [[ "$(plutil -extract status raw -o - "$result")" != "Accepted" ]]; then
    cat "$result"
    local submission
    submission=$(plutil -extract id raw -o - "$result")
    xcrun notarytool log "$submission" "${NOTARY_ARGS[@]}" || true
    return 1
  fi
  cat "$result"
}
# Staple the app before creating the DMG so the installed app also works offline.
ditto -c -k --keepParent "$APP_PATH" "$WORK_DIR/app.zip"
notarize "$WORK_DIR/app.zip" "$WORK_DIR/app-notary.json"
xcrun stapler staple "$APP_PATH"
xcrun stapler validate "$APP_PATH"
spctl --assess --type execute --verbose=4 "$APP_PATH"
mkdir "$WORK_DIR/image"
ditto "$APP_PATH" "$WORK_DIR/image/BBDown Pro.app"
ln -s /Applications "$WORK_DIR/image/Applications"
rm -f "$DMG_PATH"
hdiutil create -volname "BBDown Pro" -srcfolder "$WORK_DIR/image" -format UDZO "$DMG_PATH"
codesign --force --timestamp --sign "$APPLE_SIGN_IDENTITY" "$DMG_PATH"
notarize "$DMG_PATH" "$WORK_DIR/dmg-notary.json"
xcrun stapler staple "$DMG_PATH"
xcrun stapler validate "$DMG_PATH"
codesign --verify --strict "$DMG_PATH"
spctl --assess --type open --context context:primary-signature --verbose=4 "$DMG_PATH"
