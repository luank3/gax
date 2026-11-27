#!/usr/bin/env bash
set -euo pipefail

# Enforces Phase 0/1 binary, web-asset, startup, and RSS budgets.
AMD64_MAX=$((145 * 1024 * 1024 / 10)) # 14.5 MiB
ARM64_MAX=$((135 * 1024 * 1024 / 10)) # 13.5 MiB
WEB_GZ_MAX=$((60 * 1024))             # 60 KiB

echo "=== Checking embedded web asset gzipped budget (<= 60 KiB) ==="
web_gz=0
while IFS= read -r -d '' f; do
  gz=$(gzip -c -9 "$f" | wc -c)
  web_gz=$((web_gz + gz))
done < <(find internal/serve/ui/dist -type f -print0)
echo "web_assets_gzip=${web_gz} B (budget ${WEB_GZ_MAX} B)"
if [ "$web_gz" -gt "$WEB_GZ_MAX" ]; then
  echo "FAIL: web assets exceed budget"
  exit 1
fi

echo "=== Checking cross-compiled binary budgets ==="
for f in bin/cross/gax-*; do
  sz=$(stat -c%s "$f")
  case "$f" in
    *amd64*) limit=$AMD64_MAX ;;
    *arm64*) limit=$ARM64_MAX ;;
    *) continue ;;
  esac
  printf "%-34s %10d B (budget %d B)\n" "$f" "$sz" "$limit"
  if [ "$sz" -gt "$limit" ]; then
    echo "FAIL: $f exceeds budget"
    exit 1
  fi
done

HOST_ARCH=$(uname -m)
case "$HOST_ARCH" in
  x86_64) HOST_BIN="bin/cross/gax-linux-amd64" ;;
  aarch64|arm64) HOST_BIN="bin/cross/gax-linux-arm64" ;;
  *) echo "Unsupported host arch $HOST_ARCH"; exit 1 ;;
esac

echo "=== Checking startup (<= 15 ms) and workload RSS (<= 32 MiB) on $HOST_BIN ==="
python3 - "$HOST_BIN" << 'EOF'
import os, subprocess, sys, tempfile, time

bin_path = sys.argv[1]

# 1. Startup time (--version, 10 runs)
times = []
for _ in range(10):
    t0 = time.perf_counter()
    subprocess.run([bin_path, "--version"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    times.append((time.perf_counter() - t0) * 1000)
median_ms = sorted(times)[5]
print(f"startup_median_ms={median_ms:.2f} (budget <= 15.00)")
if median_ms > 15.0:
    sys.exit("FAIL: startup median exceeds 15 ms")

# 2. 100-turn mock session workload RSS
with tempfile.TemporaryDirectory() as tmp:
    script = os.path.join(tmp, "workload.txt")
    with open(script, "w") as f:
        for i in range(100):
            f.write(f'tool bash {{"command": "echo turn-{i}"}}\n')
            f.write("end-turn\n")
            f.write(f"text Completed turn {i}.\n")
            f.write("usage in=1000 out=50\n")
            f.write("end-turn\n")
    env = os.environ.copy()
    env["GAX_PROVIDER"] = "mock"
    env["GAX_NO_SESSION"] = "1"
    env["GAX_MOCK_SCRIPT"] = script
    env["XDG_STATE_HOME"] = tmp
    env["XDG_CONFIG_HOME"] = tmp
    p = subprocess.Popen([bin_path, "-p", "run workload"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    _, status, ru = os.wait4(p.pid, 0)
    if status != 0:
        sys.exit(f"FAIL: workload exited with {status}")
    rss_mib = ru.ru_maxrss / 1024
    print(f"workload_maxrss_mib={rss_mib:.2f} (budget <= 32.00)")
    if rss_mib > 32.0:
        sys.exit("FAIL: workload RSS exceeds 32 MiB")
EOF
