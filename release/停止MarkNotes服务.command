#!/bin/bash
SUPPORT="$HOME/Library/Application Support/MarkNotes"
if [ -f "$SUPPORT/marknotes.pid" ]; then
  kill "$(cat "$SUPPORT/marknotes.pid")" 2>/dev/null
  rm -f "$SUPPORT/marknotes.pid"
fi
pkill -f marknotes-server 2>/dev/null
echo "MarkNotes 服务已停止，可以关闭此窗口。"
