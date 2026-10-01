#!/bin/sh
set -eu
image=housefold-runtime-fixture:resumed
repo=/mnt/data/v02-repo
state=/mnt/data/module-fixture-state
log=/mnt/data/v02
mkdir -p "$log"
docker run -d --name housefold-v02-runtime --user 10001:10001 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 64m --cpus 1 -p 127.0.0.1:18099:8099 --entrypoint /housefold-runtime "$image" > "$log/runtime-id"
trap 'docker stop -t 10 housefold-v02-runtime >/dev/null 2>&1 || true' EXIT
for attempt in 1 2 3 4 5; do
 if curl -fsS -o /dev/null http://127.0.0.1:18099/healthz; then break; fi
 sleep 1
done
curl -fsS -o /dev/null http://127.0.0.1:18099/healthz
docker inspect housefold-v02-runtime --format '{{.State.Pid}}' > "$log/runtime-pid-before"
docker run --name housefold-v02-module --network none --user 10001:10001 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus 1 -v "$repo:/repo:ro" -v "$state:/tmp" -w /repo/internal/module --entrypoint /repo/internal/module/module.test "$image" -test.v -test.count=3 -test.timeout=3m > "$log/module.log" 2>&1 &
module_job=$!
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
 curl -fsS -o /dev/null http://127.0.0.1:18099/healthz
 printf 'health sample %s PASS\n' "$attempt"
 docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' housefold-v02-runtime housefold-v02-module 2>/dev/null || true
 sleep 1
done
wait "$module_job"
docker inspect housefold-v02-runtime --format '{{.State.Pid}}' > "$log/runtime-pid-after"
cmp "$log/runtime-pid-before" "$log/runtime-pid-after"
for package in execution action packageverify bridge; do
 docker run --rm --network none --user 10001:10001 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus 1 -v "$repo:/repo:ro" -v "$state:/tmp" -w "/repo/internal/$package" --entrypoint "/repo/internal/$package/$package.test" "$image" -test.v -test.timeout=3m > "$log/$package.log" 2>&1
 printf '%s tests PASS\n' "$package"
done
before=$(cut -d ' ' -f 1 /proc/uptime)
docker stop -t 10 housefold-v02-runtime >/dev/null
after=$(cut -d ' ' -f 1 /proc/uptime)
awk -v before="$before" -v after="$after" 'BEGIN {print "SIGTERM control-to-exit seconds:", after-before}'
docker inspect housefold-v02-runtime --format 'exit={{.State.ExitCode}} oom={{.State.OOMKilled}} running={{.State.Running}}'
docker inspect housefold-v02-module --format 'module exit={{.State.ExitCode}} oom={{.State.OOMKilled}}'
echo 'V02_GUEST_PASS'
