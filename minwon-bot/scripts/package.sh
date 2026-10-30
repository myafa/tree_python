#!/bin/sh
# 배포용 압축 파일을 dist/ 에 만든다. 압축마다 실행 파일, README.md, READ-ME-FIRST.txt 가 들어가고,
# 마지막에 SHA256SUMS.txt 를 만든다. Windows 는 .zip, 나머지는 실행 권한이 유지되는 .tar.gz.
set -eu
cd "$(dirname "$0")/.."
export VERSION="${VERSION:-dev}"
rm -rf dist
sh scripts/build.sh
stage=dist/stage
for f in dist/minwon-bot-*; do
  name=$(basename "$f")
  base=${name%.exe}
  rm -rf "$stage"
  mkdir -p "$stage"
  case "$name" in
    *.exe) cp "$f" "$stage/minwon-bot.exe" ;;
    *) cp "$f" "$stage/minwon-bot" && chmod 755 "$stage/minwon-bot" ;;
  esac
  cp README.md scripts/READ-ME-FIRST.txt "$stage/"
  case "$name" in
    *windows*)
      # python -m zipfile -c 는 압축하지 않고 저장만 하므로 직접 압축한다.
      python3 - "$stage" "dist/$base.zip" <<'PY'
import os, sys, zipfile
src, out = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for name in sorted(os.listdir(src)):
        z.write(os.path.join(src, name), name)
PY
      ;;
    *) tar -C "$stage" -czf "dist/$base.tar.gz" minwon-bot README.md READ-ME-FIRST.txt ;;
  esac
  rm "$f"
done
rm -rf "$stage"
cd dist
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum ./*.zip ./*.tar.gz | sed 's# \./# #' > SHA256SUMS.txt
else
  shasum -a 256 ./*.zip ./*.tar.gz | sed 's# \./# #' > SHA256SUMS.txt
fi
ls -l
