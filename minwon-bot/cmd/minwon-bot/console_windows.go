//go:build windows

package main

import "syscall"

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
)

const cpUTF8 = 65001

// setupConsole 은 콘솔 출력 코드 페이지를 UTF-8 로 바꾼다. 한국어 Windows 의 cmd.exe 는 기본이
// CP949 라서 바꾸지 않으면 안내 문구가 깨진다. 돌려주는 함수는 원래 코드 페이지로 되돌린다
// (바꾼 값이 프로그램이 끝난 뒤에도 그 창에 남지 않게).
func setupConsole() func() {
	old, _, _ := procGetConsoleOutputCP.Call()
	if old == 0 || old == cpUTF8 { // 0 이면 콘솔이 없다(출력을 파일로 보낸 경우 등)
		return func() {}
	}
	if ok, _, _ := procSetConsoleOutputCP.Call(cpUTF8); ok == 0 {
		return func() {}
	}
	return func() { _, _, _ = procSetConsoleOutputCP.Call(old) }
}
