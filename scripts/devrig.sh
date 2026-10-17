#!/usr/bin/env bash
# Starts a throwaway relay + agent with demo data for UI work.
# Usage: scripts/devrig.sh <dyrokol-binary> [workdir]
# Admin UI: http://127.0.0.1:19180  token: uitoken. Stop with scripts/devrig.sh stop.
set -euo pipefail

if [[ ${1:-} == stop ]]; then
  if [[ -f /tmp/devrig.pids ]]; then
    xargs -r kill </tmp/devrig.pids 2>/dev/null || true
    rm -f /tmp/devrig.pids
  fi
  exit 0
fi

BIN=$(realpath "$1")
W=${2:-/tmp/devrig}
rm -rf "$W"; mkdir -p "$W"; cd "$W"
H='X-Dyrokol-Api: uitoken'
B=http://127.0.0.1:19180

"$BIN" server -d srv -l 127.0.0.1:14443 -a 127.0.0.1:19180 -t uitoken >srv.log 2>&1 &
echo $! >/tmp/devrig.pids
sleep 1
"$BIN" client -d cli -s 127.0.0.1:14443 -a 127.0.0.1:19181 >cli.log 2>&1 &
echo $! >>/tmp/devrig.pids
python3 -m http.server 18080 --bind 127.0.0.1 >/dev/null 2>&1 &
echo $! >>/tmp/devrig.pids
sleep 4

post() { curl -sf -H "$H" -d "$2" "$B$1"; }
NAS=$(post /api/clients '{"label":"home-nas"}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["client"]["key_hash"])')
post /api/clients '{"label":"office-pc"}' >/dev/null
post /api/inports '{"port":18000,"label":"web"}' >/dev/null
post /api/inports '{"port":18001,"label":"ssh","deny":["10.0.0.0/8"]}' >/dev/null
post /api/inports "{\"port\":18002,\"label\":\"nas-ui\",\"machine\":\"$NAS\"}" >/dev/null
post /api/endpoints '{"port":18080,"label":"local web"}' >/dev/null
post /api/endpoints "{\"port\":5000,\"machine\":\"$NAS\",\"label\":\"nas dashboard\"}" >/dev/null
post /api/endpoints '{"type":"socks5","label":"socks5 exit"}' >/dev/null
post /api/routes '{"inport_id":1,"endpoint_id":1}' >/dev/null
post /api/routes '{"inport_id":3,"endpoint_id":2}' >/dev/null
# fan-in: three ingress ports (ids 1, 4, 5) feed the "local web" egress port (id 1)
post /api/inports '{"port":18003,"label":"web-alt"}' >/dev/null
post /api/inports '{"port":18004,"label":"web-tls"}' >/dev/null
curl -sf -X PUT -H "$H" -d '{"inport_ids":[1,4,5]}' "$B/api/endpoints/1/routes" >/dev/null
for _ in $(seq 1 40); do curl -s -o /dev/null http://127.0.0.1:18000/ || true; done
echo "devrig ready: $B (token uitoken)"
