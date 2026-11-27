#!/bin/sh
# Reads each earner's state in the guest. CashPilot runs it as root through the
# guest agent. stdin has one line per app:
#   slug|App.app|process|kind|file|arg
# kind is process, file, key or log. file is under the earner's
# ~/Library/Application Support. It prints one line per app:
#   slug|installed|running|signedin
# each y, n or u (unknown). It reads presence only: it never prints a file's
# content or a log line.
base="/Users/earner/Library/Application Support"
uid=$(id -u earner 2>/dev/null) || uid=""
while IFS='|' read -r slug app proc kind file arg; do
  [ -n "$slug" ] || continue
  inst=n; run=u; sig=u
  case "$app$file" in *..*) echo "$slug|u|u|u"; continue ;; esac
  [ -d "/Applications/$app" ] && inst=y
  if [ -n "$uid" ]; then
    if /usr/bin/pgrep -x -u "$uid" "$proc" >/dev/null 2>&1; then run=y; else run=n; fi
    f="$base/$file"
    case "$kind" in
      process) sig=$run ;;
      file) if [ -s "$f" ]; then sig=y; else sig=n; fi ;;
      key) if [ -s "$f" ] && /usr/bin/grep -qF "\"$arg\"" "$f"; then sig=y; else sig=n; fi ;;
      log) if [ -s "$f" ] && /usr/bin/grep -qiF "$arg" "$f"; then sig=y; else sig=n; fi ;;
    esac
  fi
  echo "$slug|$inst|$run|$sig"
done
