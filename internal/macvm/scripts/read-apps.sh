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
# marker prints y for a marker with content, n for a missing or empty one, and
# u when the earner cannot look: an unreadable file, a broken symlink, a
# folder on the way it cannot open, or a sudo that failed.
marker() {
  m=$(as_earner /bin/sh -c '
    f=$1
    if [ -f "$f" ] && [ -r "$f" ]; then
      if [ -s "$f" ]; then echo y; else echo n; fi
      exit 0
    fi
    if [ -e "$f" ] || [ -L "$f" ]; then echo u; exit 0; fi
    d=${f%/*}
    while [ -n "$d" ] && [ ! -d "$d" ]; do d=${d%/*}; done
    if [ -x "${d:-/}" ]; then echo n; else echo u; fi' sh "$1") || m=u
  case "$m" in y|n) echo "$m" ;; *) echo u ;; esac
}
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
      file) sig=$(marker "$f") ;;
      # grep exits 1 for no match and 2 when it cannot read the file: a
      # file the app replaced under us proves nothing, so that is unknown.
      key) sig=$(marker "$f")
           if [ "$sig" = y ]; then
             as_earner /usr/bin/grep -qF "\"$arg\"" "$f"
             case $? in 0) sig=y ;; 1) sig=n ;; *) sig=u ;; esac
           fi ;;
      # The last line with the words decides, and "not <words>" is a no.
      log) sig=$(marker "$f")
           if [ "$sig" = y ]; then
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
