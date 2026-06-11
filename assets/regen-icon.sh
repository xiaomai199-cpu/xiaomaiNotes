#!/bin/bash
# 从 assets/icon.svg 重新生成 assets/AppIcon.icns（需要 rsvg-convert：brew install librsvg）
set -euo pipefail
cd "$(dirname "$0")"

TMP="$(mktemp -d)/AppIcon.iconset"
mkdir -p "$TMP"
for s in 16 32 128 256 512; do
  rsvg-convert -w "$s" -h "$s" icon.svg -o "$TMP/icon_${s}x${s}.png"
  rsvg-convert -w "$((s * 2))" -h "$((s * 2))" icon.svg -o "$TMP/icon_${s}x${s}@2x.png"
done
iconutil -c icns "$TMP" -o AppIcon.icns
rm -rf "$(dirname "$TMP")"
echo "已生成 assets/AppIcon.icns"
