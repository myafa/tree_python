#!/usr/bin/env bash
# 참고용 시험: 설치된 Chrome/Edge 를 창 없이 띄워 화면 저장이 이 OS 에서 동작하는지 본다.
#   1) 명령(snapshot)으로 저장   2) 앱 화면이 쓰는 웹 API 로 저장(브라우저 열기 → 지금 화면 저장 → 닫기)
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

case "$(uname -s)" in
  MINGW* | MSYS* | CYGWIN*) PY=python ;;
  *) PY=$(command -v python3 || command -v python) ;;
esac
"$PY" -m http.server 18799 --bind 127.0.0.1 --directory "$HERE/smoke-site" > "$WORK/http.log" 2>&1 &
HTTP_PID=$!
APP_PID=""
trap 'kill "$HTTP_PID" ${APP_PID:+"$APP_PID"} 2>/dev/null || true' EXIT
for _ in $(seq 1 20); do
  curl -fsS http://127.0.0.1:18799/ -o /dev/null 2>/dev/null && break
  sleep 1
done

fail() {
  echo "실패: $*"
  [ -f "$WORK/server.log" ] && { echo "--- 앱 실행 기록"; cat "$WORK/server.log"; }
  exit 1
}

# 저장한 파일에 가려야 할 값(시험 사이트에 심어 둔 값)이 남았는지 본다.
check_clean() {
  if grep -rqE 'SECRET-|010-1234-5678|홍길동|jsessionid' "$@"; then
    grep -rnE 'SECRET-|010-1234-5678|홍길동|jsessionid' "$@" | head
    fail "저장한 파일에 가려야 할 값이 남았습니다"
  fi
}

# 붙여넣는 요약에는 목록(표) 안 링크의 글자(내 민원 제목 등)가 없어야 한다.
# page.html 은 이 PC 에만 두는 파일이라 화면 글자가 남는다(형식 기반 가림과 가릴 낱말만 적용).
check_no_list_text() {
  if grep -qF LISTTEXT "$@"; then
    grep -nF LISTTEXT "$@" | head
    fail "요약에 목록 글자가 남았습니다"
  fi
}

echo "== 1) 명령으로 화면 저장"
export MINWON_HOME="$WORK/home"
EXTRA=()
case "$(uname -s)" in Linux*) EXTRA+=(-no-sandbox) ;; esac
[ -n "${MINWON_SMOKE_BROWSER:-}" ] && EXTRA+=(-browser "$MINWON_SMOKE_BROWSER")

printf '\nq\n' | "$BIN" snapshot -headless ${EXTRA[@]+"${EXTRA[@]}"} \
  -url http://127.0.0.1:18799/ -out "$WORK/out" -mask "이름:홍길동"

outline=$(ls "$WORK"/out/001-*/outline.txt)
echo "--- $outline"
cat "$outline"
# 확인 문자열은 영문만 쓴다(Windows 에서 명령 인자의 한글이 깨져도 판정이 흔들리지 않게).
#  {{NAME_1}}: 이름이 가려짐, fn_view: 목록 링크의 동작은 남고 글자(LISTTEXT)는 빠짐
for want in '[form]' '== frame-1' '{{NAME_1}}' "onclick=\"fn_view('2AA-1')\""; do
  grep -qF "$want" "$outline" || fail "화면 요약에 $want 가 없습니다"
done
check_clean "$WORK/out"
check_no_list_text "$WORK"/out/*/outline.txt "$WORK"/out/outline-*.txt
echo "확인: 명령으로 저장"

echo "== 2) 앱 화면(웹 API)으로 화면 저장"
export MINWON_HOME="$WORK/home-web"
export MINWON_SNAPSHOT_HEADLESS=1
case "$(uname -s)" in Linux*) export MINWON_SNAPSHOT_NO_SANDBOX=1 ;; esac
[ -n "${MINWON_SMOKE_BROWSER:-}" ] && export MINWON_SNAPSHOT_BROWSER="$MINWON_SMOKE_BROWSER"
"$BIN" -no-browser -port 18746 > "$WORK/server.log" 2>&1 &
APP_PID=$!

URL=""
for _ in $(seq 1 30); do
  URL=$(grep -oE 'http://127\.0\.0\.1:[0-9]+' "$WORK/server.log" 2>/dev/null | head -1 || true)
  if [ -n "$URL" ] && curl -fsS "$URL/snapshot" -o "$WORK/snapshot.html" 2>/dev/null; then
    break
  fi
  URL=""
  sleep 1
done
[ -n "$URL" ] || fail "앱이 30초 안에 뜨지 않았습니다"
grep -q 'data-action="snap-capture"' "$WORK/snapshot.html" || fail "화면 저장 페이지 내용이 이상합니다"
TOK=$(grep -oE 'name="csrf" content="[0-9a-f]+"' "$WORK/snapshot.html" | sed -E 's/.*content="([0-9a-f]+)"/\1/')
[ -n "$TOK" ] || fail "화면에서 요청 토큰을 읽지 못했습니다"

# api <동작> [JSON] : 화면의 버튼이 부르는 것과 같은 요청. 응답은 $WORK/<동작>.json, 출력은 상태 코드.
api() {
  local data=${2:-}
  [ -n "$data" ] || data='{}'
  curl -sS -X POST -H 'Content-Type: application/json' -H "X-CSRF-Token: $TOK" \
    -d "$data" -o "$WORK/$1.json" -w '%{http_code}' "$URL/api/snapshot/$1"
}

# 이름은 \u 표기로 보낸다(Windows 에서 명령 인자의 한글이 깨지지 않게).
code=$(api mask '{"name":"홍길동"}')
[ "$code" = 200 ] || fail "가릴 이름 추가 응답 $code: $(cat "$WORK/mask.json")"
code=$(api start '{"url":"http://127.0.0.1:18799/"}')
[ "$code" = 200 ] && grep -q '"open":true' "$WORK/start.json" || fail "브라우저 열기 응답 $code: $(cat "$WORK/start.json")"
echo "확인: 브라우저 열기"
code=$(api capture)
[ "$code" = 200 ] && grep -q '"saved":1' "$WORK/capture.json" || fail "지금 화면 저장 응답 $code: $(cat "$WORK/capture.json")"
for want in '[form]' '== frame-1' '{{NAME_1}}' "onclick=\\\"fn_view('2AA-1')\\\""; do
  grep -qF "$want" "$WORK/capture.json" || fail "모은 글에 $want 가 없습니다: $(cat "$WORK/capture.json")"
done
echo "확인: 지금 화면 저장"
code=$(api stop)
[ "$code" = 200 ] && grep -q '"open":false' "$WORK/stop.json" || fail "브라우저 닫기 응답 $code"
echo "확인: 브라우저 닫기"
[ -n "$(ls -A "$MINWON_HOME/browser-profile" 2>/dev/null)" ] || fail "닫은 뒤 브라우저 프로필(로그인 상태)이 남아 있지 않습니다"
echo "확인: 닫은 뒤에도 프로필 유지"

outline=$(ls "$MINWON_HOME"/snapshots/*/001-*/outline.txt)
echo "--- $outline"
cat "$outline"
check_clean "$WORK/capture.json" "$MINWON_HOME/snapshots"
check_no_list_text "$WORK/capture.json" "$MINWON_HOME"/snapshots/*/*/outline.txt
echo "확인: 앱 화면으로 저장한 파일에 가릴 값 없음"

echo "브라우저 시험 통과 (명령, 앱 화면)"
