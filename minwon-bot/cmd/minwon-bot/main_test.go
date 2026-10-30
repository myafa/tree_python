package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "abc1234"
	got := versionString()
	for _, want := range []string{"minwon-bot abc1234", runtime.GOOS + "/" + runtime.GOARCH, runtime.Version()} {
		if !strings.Contains(got, want) {
			t.Errorf("%q 에 %q 없음", got, want)
		}
	}
}

func TestSetupConsoleRestoreIsSafe(t *testing.T) {
	restore := setupConsole()
	restore()
	restore() // 두 번 불러도 문제없어야 한다
}
