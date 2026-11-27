#!/bin/sh
# Lab only: the fence probe. It runs INSIDE the macOS guest, through the guest
# agent, and needs nothing that a fresh macOS install lacks (sh, nc, curl, dig,
# ping, dns-sd).
#
#   tart exec -i <vm> /bin/sh -c "$(cat probe.sh)" probe <token> < targets.txt
#
# targets.txt holds one target per line (no addresses live in this file):
#   <label> tcp  <address> <port>
#   <label> udp  <address> <port>
#   <label> icmp <address> -
#   <label> mdns <service-type> -        e.g. _cpcanary._tcp
#   <label> dns  <name> -                prints the answer, connects to nothing
#   <label> dnsat <server> <name>        asks that server directly (dig @server)
#   <label> http <url> -                 a positive control, prints the HTTP code
#   <label> echo <url> -                 an egress echo, prints the address it saw
#
# Every TCP and UDP probe sends "<token>-<label>". The canary on the far side
# logs what arrives. The probe's own verdict (connected / refused / timeout) is
# only a hint: an arrival of the token at the canary is the evidence.
TOKEN="$1"
[ -n "$TOKEN" ] || { echo "usage: probe.sh <token>  (targets on stdin)" >&2; exit 2; }

tcp() {
  out=$(printf '%s\n' "$TOKEN-$1" | /usr/bin/nc -G 3 -w 2 "$2" "$3" 2>&1); rc=$?
  if [ $rc -eq 0 ]; then echo connected
  elif echo "$out" | grep -qi refused; then echo refused
  elif echo "$out" | grep -qi -e "no route" -e unreachable -e "not permitted"; then echo "unreachable"
  else echo timeout
  fi
}

udp() {
  printf '%s\n' "$TOKEN-$1" | /usr/bin/nc -u -w 1 "$2" "$3" >/dev/null 2>&1 && echo sent || echo send-failed
}

icmp() {
  case "$2" in
    *:*) /sbin/ping6 -c 2 -i 1 "$2" >/dev/null 2>&1 ;;
    *)   /sbin/ping -c 2 -t 3 "$2" >/dev/null 2>&1 ;;
  esac && echo replied || echo no-reply
}

mdns() {
  # Flush the guest's own cache first (needs root), so only answers that
  # arrive during this browse count. The guest's own services are left out.
  [ "$(id -u)" = 0 ] && /usr/bin/killall -HUP mDNSResponder 2>/dev/null && sleep 1
  f=$(mktemp)
  /usr/bin/dns-sd -B "$2" local. > "$f" 2>&1 &
  p=$!; sleep 6; kill $p 2>/dev/null; wait $p 2>/dev/null
  # The guest registers under both names (ssh and rfb use the ComputerName).
  me=$(/usr/sbin/scutil --get LocalHostName 2>/dev/null)
  me2=$(/usr/sbin/scutil --get ComputerName 2>/dev/null)
  names=$(grep " Add " "$f" | awk '{for(i=7;i<=NF;i++) printf "%s%s", $i, (i<NF?" ":""); print ""}' | grep -v -x -F -e "$me" -e "$me2" | sort -u | tr '\n' ',')
  rm -f "$f"
  [ -n "$names" ] && echo "seen:${names%,}" || echo none
}

while read -r label kind target port; do
  case "$label" in ''|'#'*) continue ;; esac
  case "$kind" in
    tcp)  r=$(tcp "$label" "$target" "$port") ;;
    udp)  r=$(udp "$label" "$target" "$port") ;;
    icmp) r=$(icmp "$label" "$target") ;;
    mdns) r=$(mdns "$label" "$target") ;;
    dns)  r=$(/usr/bin/dig +short +time=3 +tries=1 "$target" 2>&1 | tr '\n' ' ') ; r="answer:[${r% }]" ;;
    dnsat) r=$(/usr/bin/dig +short +time=3 +tries=1 "@$target" "$port" 2>&1 | tr '\n' ' ') ; r="answer:[${r% }]" ;;
    http) r=$(/usr/bin/curl -s -o /dev/null -m 8 -w '%{http_code}' "$target" 2>&1) ; r="http:$r" ;;
    echo) r=$(/usr/bin/curl -s -m 8 "$target" 2>&1 | tr -d '\n') ; r="egress:$r" ;;
    *)    r="unknown-kind" ;;
  esac
  printf '%s\t%s\t%s\t%s\t%s\n' "$label" "$kind" "$target" "$port" "$r"
done
