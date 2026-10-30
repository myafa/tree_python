//go:build !windows

package main

// setupConsole 은 Windows 가 아니면 할 일이 없다(터미널이 이미 UTF-8).
func setupConsole() func() { return func() {} }
