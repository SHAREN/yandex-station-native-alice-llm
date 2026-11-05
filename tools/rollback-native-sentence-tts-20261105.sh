#!/bin/sh
# Rollback for Lite 1 native sentence-driven TTS build, 2026-11-05.
# Run on the station as root; does not modify the stock maind firmware.
# Requires the verified /tmp/quasar-bridge-0.4.5f.gz backup.
set -u

state=/data/opt/quasar-bridge
binary=/bkupgrade/data/quazar/betteryam/data/plugins/quasar-bridge/bin/quasar-bridge
backup=/tmp/quasar-bridge-0.4.5f.gz
pidfile=$state/quasar-bridge.pid
newsha=5ba6584fc1bff9aa751dda3902b29b7ed41b7104de437392ade90a7d2ecf067f
oldsha=2a45c6cffc7e4469bd4b1d68f597c2f9dafa759f707763e8dae9de681b634206

hash() { sha256sum "$1" | cut -d' ' -f1; }
connected() { netstat -ant 2>/dev/null | grep 8443 | grep -q ESTABLISHED; }

if [ "$(hash "$binary")" != "$newsha" ]; then
  echo "ABORT: unexpected installed binary; leave it unchanged"
  exit 2
fi
if [ "$(gzip -dc "$backup" | sha256sum | cut -d' ' -f1)" != "$oldsha" ]; then
  echo "ABORT: backup is missing or has unexpected SHA256"
  exit 3
fi
echo PRECHECK_OK

pid=$(cat "$pidfile" 2>/dev/null || true)
if [ -n "$pid" ]; then
  kill "$pid" 2>/dev/null || true
  n=0
  while kill -0 "$pid" 2>/dev/null && [ "$n" -lt 100 ]; do sleep 0.1; n=$((n+1)); done
  if kill -0 "$pid" 2>/dev/null; then echo "ABORT: Bridge did not exit"; exit 4; fi
fi
rm -f "$pidfile" "$binary"
gzip -dc "$backup" > "$binary" || { echo "FAILED: unpack old binary"; exit 5; }
chmod 755 "$binary"
if [ "$(hash "$binary")" != "$oldsha" ]; then
  echo "FAILED: restored binary hash mismatch"
  exit 6
fi
/etc/init.d/S98zzbetteryam start || { echo "FAILED: restart Bridge"; exit 7; }

n=0
while [ "$n" -lt 15 ]; do
  if connected; then break; fi
  sleep 1
  n=$((n+1))
done
if ! connected; then
  mpid=$(ps | grep '/system/vendor/quasar/maind --platformd' | grep -v grep | awk 'NR==1 {print $1}')
  if [ -n "$mpid" ]; then kill -TERM "$mpid" 2>/dev/null || true; fi
  n=0
  while [ "$n" -lt 60 ]; do
    if connected; then break; fi
    sleep 1
    n=$((n+1))
  done
fi
echo "RESTORED_SHA $(hash "$binary")"
echo "HEALTH $(wget -q -T 2 -O - http://127.0.0.1:8443/healthz 2>/dev/null || true)"
if connected; then
  echo PRODUCTION_0_4_5F_RESTORED
  exit 0
fi
echo "WARNING: original binary restored but stock maind connection not established"
exit 8
