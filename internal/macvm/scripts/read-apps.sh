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
as_earner() { /usr/bin/sudo -n -u earner "$@"; }
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
      # The markers are in the earner's own folder, where the app can put a
      # symlink, so they are read with the earner's rights, never root's.
      file) if as_earner /bin/test -s "$f"; then sig=y; else sig=n; fi ;;
      # grep exits 1 for no match and 2 when it cannot read the file: a
      # file the app replaced under us proves nothing, so that is unknown.
      key) sig=n
           if as_earner /bin/test -s "$f"; then
             as_earner /usr/bin/grep -qF "\"$arg\"" "$f"
             case $? in 0) sig=y ;; 1) sig=n ;; *) sig=u ;; esac
           fi ;;
      # The last line with the words decides, and "not <words>" is a no.
      log) sig=n
           if as_earner /bin/test -s "$f"; then
             lines=$(as_earner /usr/bin/grep -iF "$arg" "$f")
             case $? in
               0) last=$(printf '%s\n' "$lines" | /usr/bin/tail -n 1)
                  if printf '%s' "$last" | /usr/bin/grep -qiF "not $arg"; then sig=n; else sig=y; fi ;;
               1) sig=n ;;
               *) sig=u ;;
             esac
           fi ;;
    esac
  fi
  echo "$slug|$inst|$run|$sig"
done
