#!/usr/bin/env bash
# Live latency demo (HUB-353): the same small JSON POST to a public echo API, N times directly and
# N times through a local Hub, then p50 / p95 / p99 for both and the difference. See README.md.
#
#   examples/latency-demo/run.sh                      # N=200, C=4 against https://postman-echo.com
#   N=500 C=8 examples/latency-demo/run.sh            # more samples (be polite: it is a free service)
#   LATENCY_TARGET=mock examples/latency-demo/run.sh  # offline: mockvendor answering in 50 ms
#
# N        requests per path (default 200)          C       concurrent workers per path (default 4)
# ROUNDS   direct/Hub alternations (default 4)      WARMUP  discarded requests per worker (default 2)
# HUB_PG_DSN, HUB_REDIS_ADDR as for the other examples (Postgres + Redis must be up).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
# shellcheck source=../lib/local-hub.sh
source "$root/examples/lib/local-hub.sh"

N="${N:-200}"
C="${C:-4}"
ROUNDS="${ROUNDS:-4}"
WARMUP="${WARMUP:-2}"
target="${LATENCY_TARGET:-public}"
key=hk_test_example_latency_demo_local_only # local test key (tenant latency-demo)
[ "$N" -ge "$((C * ROUNDS))" ] || hub_die "N ($N) must be at least C × ROUNDS ($((C * ROUNDS)))"

hub_setup "${HUB_DEMO_STATE_DIR:-$root/.demo/latency-demo}" "$here"
hub_set_creds echo-public client_tag=latency-demo
case "$target" in
  public)
    direct_url="https://postman-echo.com/post"
    hub_start
    ;;
  mock)
    direct_url="http://$MOCKVENDOR_ADDR/post"
    hub_set_region echo-public.yaml mock
    hub_start --mockvendor
    mock_scenario "$here/mockvendor/echo.json"
    ;;
  *) hub_die "LATENCY_TARGET must be public or mock, got $target" ;;
esac
hub_url="$HUB_URL/v1/integrations/int_latency_echo/invoke"
out="$HUB_DEMO_STATE/results"
rm -rf "$out" && mkdir -p "$out"

# One curl process per worker runs its requests back to back over one kept-alive connection, so
# both paths pay connection setup once (during the warm-up), not per request.
# worker PATH_NAME URL AUTH WORKER COUNT ROUND
worker() {
  local name="$1" url="$2" auth="$3" w="$4" count="$5" round="$6" cfg i
  cfg="$out/$name-$round-$w.curl"
  : >"$cfg"
  for i in $(seq 1 "$((WARMUP + count))"); do
    {
      [ "$i" -gt 1 ] && printf 'next\n'
      printf 'url = "%s"\nrequest = "POST"\nsilent\noutput = "/dev/null"\n' "$url"
      printf 'header = "Content-Type: application/json"\n'
      [ -n "$auth" ] && printf 'header = "Authorization: Bearer %s"\n' "$auth"
      printf 'data = "{\\"message\\": \\"latency-demo\\", \\"seq\\": %d}"\n' "$i"
      printf 'write-out = "%%{http_code} %%{time_total}\\n"\n'
    } >>"$cfg"
  done
  curl --config "$cfg" | tail -n +"$((WARMUP + 1))" >>"$out/$name.txt"
}

run_path() {
  local name="$1" url="$2" auth="$3" round="$4" per w pids=()
  per=$((N / ROUNDS / C))
  for w in $(seq 1 "$C"); do
    worker "$name" "$url" "$auth" "$w" "$per" "$round" &
    pids+=("$!")
  done
  wait "${pids[@]}"
}

metrics() { curl -fsS "http://$HUB_DEMO_OPS/metrics" | grep '^hub_overhead_ms_' | grep 'integration="int_latency_echo"' || true; }

before="$(metrics)"
hub_log "measuring: $((N / ROUNDS / C * ROUNDS * C)) requests per path, $C workers, $ROUNDS alternating rounds ($target)"
for r in $(seq 1 "$ROUNDS"); do
  run_path direct "$direct_url" "" "$r"
  run_path hub "$hub_url" "$key" "$r"
done
after="$(metrics)"

python3 - "$out/direct.txt" "$out/hub.txt" "$direct_url" "$hub_url" "$target" <<PY
import math, re, sys
direct_file, hub_file, direct_url, hub_url, target = sys.argv[1:6]

def load(path):
    ok, bad = [], 0
    for line in open(path):
        code, secs = line.split()
        if code == "200":
            ok.append(float(secs) * 1000)
        else:
            bad += 1
    return sorted(ok), bad

def pct(xs, p):  # nearest rank
    return xs[max(0, math.ceil(p / 100 * len(xs)) - 1)] if xs else float("nan")

d, dbad = load(direct_file)
h, hbad = load(hub_file)
print()
print(f"Target: {target}   direct: POST {direct_url}   via Hub: POST {hub_url}")
print(f"{'':10}{'n':>6}{'errors':>8}{'p50 ms':>10}{'p95 ms':>10}{'p99 ms':>10}{'mean ms':>10}")
for name, xs, bad in (("direct", d, dbad), ("via Hub", h, hbad)):
    mean = sum(xs) / len(xs) if xs else float("nan")
    print(f"{name:10}{len(xs):>6}{bad:>8}{pct(xs,50):>10.1f}{pct(xs,95):>10.1f}{pct(xs,99):>10.1f}{mean:>10.1f}")
dm = (sum(h) / len(h) - sum(d) / len(d)) if d and h else float("nan")
print(f"{'difference':10}{'':>6}{'':>8}{pct(h,50)-pct(d,50):>+10.1f}{pct(h,95)-pct(d,95):>+10.1f}{pct(h,99)-pct(d,99):>+10.1f}{dm:>+10.1f}")

# The Hub's own measurement of its overhead (server time minus vendor time), from hub_overhead_ms.
def parse(text):
    buckets, total, count = {}, 0.0, 0.0
    for line in text.splitlines():
        m = re.match(r'hub_overhead_ms_(bucket|sum|count)\{([^}]*)\} (\S+)', line)
        if not m:
            continue
        kind, labels, value = m.group(1), m.group(2), float(m.group(3))
        if kind == "bucket":
            le = re.search(r'le="([^"]+)"', labels).group(1)
            buckets[float("inf") if le == "+Inf" else float(le)] = buckets.get(float("inf") if le == "+Inf" else float(le), 0) + value
        elif kind == "sum":
            total += value
        else:
            count += value
    return buckets, total, count
b0, s0, c0 = parse("""$before""")
b1, s1, c1 = parse("""$after""")
n = c1 - c0
if n > 0:
    les = sorted(b1)
    cum = [b1[le] - b0.get(le, 0) for le in les]
    def q(p):
        rank, prev_le, prev_c = p * n, 0.0, 0.0
        for le, c in zip(les, cum):
            if c >= rank:
                if math.isinf(le):
                    return f">{prev_le:g}"
                return f"{prev_le + (le - prev_le) * (rank - prev_c) / max(c - prev_c, 1e-9):.2f}"
            prev_le, prev_c = le, c
        return "?"
    print(f"\nHub's own overhead (hub_overhead_ms over {int(n)} requests, warm-up included): mean {(s1 - s0) / n:.2f} ms, p50 ~{q(0.5)}, p95 ~{q(0.95)}, p99 ~{q(0.99)} ms (bucket estimates)")
print("\nRead the difference row with care: internet jitter between runs is usually larger than the Hub's overhead.")
print("Compare the Hub's own overhead line and the controlled mockvendor numbers in docs/tech/LATENCY.md.")
PY
hub_log "raw samples (http_code seconds): $out/direct.txt, $out/hub.txt"
