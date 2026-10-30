#!/usr/bin/env bash
# 참고용 시험: 설치된 Chrome/Edge 를 창 없이 띄워 snapshot 이 이 OS 에서 동작하는지 본다.
# 사용법: smoke-browser.sh <실행 파일>   MINWON_SMOKE_BROWSER 로 브라우저 경로를 줄 수 있다.
set -euo pipefail
BIN=$1
HERE=$(cd "$(dirname "$0")" && pwd)

base=${RUNNER_TEMP:-}
if [ -n "$base" ] && command -v cygpath >/dev/null 2>&1; then
  base=$(cygpath -m "$base")
fi
[ -n "$base" ] || base=$(mktemp -d)
WORK="$base/minwon-snap"
rm -rf "$WORK"
mkdir -p "$WORK"
export MINWON_HOME="$WORK/home"

case "$(uname -s)" in
  MINGW* | MSYS* | CYGWIN*) PY=python ;;
  *) PY=$(command -v python3 || command -v python) ;;
esac
"$PY" -m http.server 18799 --bind 127.0.0.1 --directory "$HERE/smoke-site" > "$WORK/http.log" 2>&1 &
HTTP_PID=$!
trap 'kill "$HTTP_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 20); do
  curl -fsS http://127.0.0.1:18799/ -o /dev/null 2>/dev/null && break
  sleep 1
done

EXTRA=()
case "$(uname -s)" in Linux*) EXTRA+=(-no-sandbox) ;; esac
[ -n "${MINWON_SMOKE_BROWSER:-}" ] && EXTRA+=(-browser "$MINWON_SMOKE_BROWSER")

printf '\nq\n' | "$BIN" snapshot -headless ${EXTRA[@]+"${EXTRA[@]}"} \
  -url http://127.0.0.1:18799/ -out "$WORK/out" -mask "이름:홍길동"

outline=$(ls "$WORK"/out/001-*/outline.txt)
echo "--- $outline"
cat "$outline"
grep -q '\[form\]' "$outline" || { echo "실패: 화면 요약에 폼이 없습니다"; exit 1; }
grep -q '== frame-1' "$outline" || { echo "실패: iframe 을 읽지 못했습니다"; exit 1; }
if grep -rqE 'SECRET-|010-1234-5678|홍길동|jsessionid' "$WORK/out"; then
  echo "실패: 저장한 파일에 가려야 할 값이 남았습니다"
  grep -rnE 'SECRET-|010-1234-5678|홍길동|jsessionid' "$WORK/out" | head
  exit 1
fi
echo "브라우저 시험 통과"
