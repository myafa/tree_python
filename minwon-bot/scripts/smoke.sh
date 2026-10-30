#!/usr/bin/env bash
# 내려받은 실행 파일이 이 OS 에서 뜨는지와 기본 보안 검사를 확인한다.
# 사용법: smoke.sh <실행 파일>   (Windows 는 Git Bash 에서 실행)
set -euo pipefail
BIN=$1

base=${RUNNER_TEMP:-}
if [ -n "$base" ] && command -v cygpath >/dev/null 2>&1; then
  base=$(cygpath -m "$base") # Windows 경로를 bash 와 실행 파일이 함께 쓸 수 있는 형태로
fi
[ -n "$base" ] || base=$(mktemp -d)
WORK="$base/minwon-smoke"
rm -rf "$WORK"
mkdir -p "$WORK"
export MINWON_HOME="$WORK/home"

fail() { echo "실패: $*"; [ -f "$WORK/server.log" ] && { echo "--- 실행 기록"; cat "$WORK/server.log"; }; exit 1; }

echo "== 버전"
"$BIN" -version | tee "$WORK/version.txt"
grep -q '^minwon-bot ' "$WORK/version.txt" || fail "버전 출력이 이상합니다"

echo "== 서버 기동"
"$BIN" -no-browser -port 18745 > "$WORK/server.log" 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT

# 포트가 사용 중이면 앱이 빈 포트로 옮기므로, 주소는 실행 기록에서 읽는다.
URL=""
for _ in $(seq 1 30); do
  URL=$(grep -oE 'http://127\.0\.0\.1:[0-9]+' "$WORK/server.log" 2>/dev/null | head -1 || true)
  if [ -n "$URL" ] && curl -fsS "$URL/" -o "$WORK/home.html" 2>/dev/null; then
    break
  fi
  URL=""
  sleep 1
done
[ -n "$URL" ] || fail "서버가 30초 안에 뜨지 않았습니다"
echo "주소: $URL"

grep -q 'href="/inbox"' "$WORK/home.html" || fail "첫 화면 내용이 이상합니다"
echo "확인: 첫 화면"

code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example.com' "$URL/")
[ "$code" = 403 ] || fail "다른 호스트 이름의 접근을 막지 못했습니다($code)"
echo "확인: 다른 호스트 이름 차단(403)"

code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$URL/api/style/reset")
[ "$code" = 403 ] || fail "토큰 없는 요청을 막지 못했습니다($code)"
echo "확인: 토큰 없는 요청 차단(403)"

for p in /static/app.js /static/app.css /settings /inbox /cases/new; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$URL$p")
  [ "$code" = 200 ] || fail "$p 응답 $code"
done
echo "확인: 정적 파일과 화면(설정, 보고함, 새 사안)"

[ -f "$MINWON_HOME/minwon.db" ] || fail "데이터 파일이 만들어지지 않았습니다"
echo "확인: 데이터 파일 생성"

echo "서버 시험 통과"
