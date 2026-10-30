#!/bin/sh
# Windows, macOS, Linux 용 실행 파일을 dist/ 에 만든다. cgo 를 쓰지 않으므로 어느 PC 에서든 빌드된다.
# VERSION 환경변수가 있으면 실행 파일에 버전으로 넣는다(minwon-bot -version 으로 확인).
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
for target in windows/amd64 darwin/arm64 darwin/amd64 linux/amd64; do
  os=${target%/*}; arch=${target#*/}
  out="dist/minwon-bot-$os-$arch"
  [ "$os" = windows ] && out="$out.exe"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION:-dev}" -o "$out" ./cmd/minwon-bot
  echo "빌드: $out"
done
