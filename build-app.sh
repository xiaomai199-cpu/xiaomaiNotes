#!/bin/bash
# 打包 MarkNotes：
#   ./build-app.sh        生成 dist/marknotes 单文件可执行程序 + dist/MarkNotes.app（macOS 双击运行）
#
# MarkNotes.app 的数据目录：
#   默认  iCloud 云盘下的「科研笔记--麦子」（~/Library/Mobile Documents/com~apple~CloudDocs/科研笔记--麦子）
#   自定义 在 ~/Library/Application Support/MarkNotes 下创建 datadir.txt，第一行写上想用的数据目录绝对路径
#   （命令行运行 dist/marknotes 时则直接用 -data <目录> 指定）
set -euo pipefail
cd "$(dirname "$0")"

APP_NAME="MarkNotes"
DIST="dist"

rm -rf "$DIST"
mkdir -p "$DIST"

echo "==> 编译单文件可执行程序 $DIST/marknotes"
go build -ldflags "-s -w" -o "$DIST/marknotes" .

APP="$DIST/$APP_NAME.app"
MACOS_DIR="$APP/Contents/MacOS"
RES_DIR="$APP/Contents/Resources"
mkdir -p "$MACOS_DIR" "$RES_DIR"

# 麦穗图标（源文件 assets/icon.svg，用 assets/regen-icon.sh 重新生成）
cp assets/AppIcon.icns "$RES_DIR/AppIcon.icns"

# 注意：macOS 文件系统不区分大小写，二进制不能与启动器 "MarkNotes" 重名
cp "$DIST/marknotes" "$MACOS_DIR/marknotes-server"

cat > "$MACOS_DIR/$APP_NAME" <<'LAUNCHER'
#!/bin/bash
# MarkNotes.app 启动器：确定数据目录 -> 后台启动服务 -> 打开浏览器 -> 立即退出。
# 启动器必须退出，否则 macOS 认为 app 仍在运行，再次双击不会有任何反应。
# 服务进程脱离启动器常驻后台，下次双击时检测到已在运行则直接打开浏览器。
DIR="$(cd "$(dirname "$0")" && pwd)"
SUPPORT="$HOME/Library/Application Support/MarkNotes"
mkdir -p "$SUPPORT"

# 默认数据目录：iCloud 云盘下的「科研笔记--麦子」；可用 datadir.txt 覆盖
DATA_DIR="$HOME/Library/Mobile Documents/com~apple~CloudDocs/科研笔记--麦子"
if [ -f "$SUPPORT/datadir.txt" ]; then
  CUSTOM="$(head -n1 "$SUPPORT/datadir.txt" | sed 's/[[:space:]]*$//')"
  [ -n "$CUSTOM" ] && DATA_DIR="$CUSTOM"
fi

PORT=44444
URL="http://localhost:$PORT/"

# 已有实例在运行时直接打开浏览器
if curl -s -o /dev/null --max-time 1 "$URL"; then
  open "$URL"
  exit 0
fi

nohup "$DIR/marknotes-server" -data "$DATA_DIR" -addr ":$PORT" >> "$SUPPORT/marknotes.log" 2>&1 &
echo $! > "$SUPPORT/marknotes.pid"
disown

for _ in $(seq 1 20); do
  curl -s -o /dev/null --max-time 1 "$URL" && break
  sleep 0.3
done
open "$URL"
exit 0
LAUNCHER
chmod +x "$MACOS_DIR/$APP_NAME" "$MACOS_DIR/marknotes-server"

# 双击停止后台服务的小工具
cat > "$DIST/停止MarkNotes服务.command" <<'STOP'
#!/bin/bash
SUPPORT="$HOME/Library/Application Support/MarkNotes"
if [ -f "$SUPPORT/marknotes.pid" ]; then
  kill "$(cat "$SUPPORT/marknotes.pid")" 2>/dev/null
  rm -f "$SUPPORT/marknotes.pid"
fi
pkill -f marknotes-server 2>/dev/null
echo "MarkNotes 服务已停止，可以关闭此窗口。"
STOP
chmod +x "$DIST/停止MarkNotes服务.command"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>$APP_NAME</string>
  <key>CFBundleDisplayName</key><string>$APP_NAME</string>
  <key>CFBundleIdentifier</key><string>local.marknotes.app</string>
  <key>CFBundleVersion</key><string>1.0</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>$APP_NAME</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>LSUIElement</key><true/>
</dict>
</plist>
PLIST

echo "==> 完成："
echo "    $DIST/marknotes        单文件可执行程序（./marknotes -data <数据目录> [-addr :端口]）"
echo "    $APP    macOS 双击运行（数据默认存放在 iCloud 云盘/科研笔记--麦子）"
echo "    $DIST/停止MarkNotes服务.command    双击停止后台服务"
