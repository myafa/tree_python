package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ClaudeCode 는 이 PC 에 설치되어 로그인된 `claude -p` 를 호출한다.
// 구독 한도 안에서 처리되며 별도의 API 크레딧을 쓰지 않는다.
type ClaudeCode struct {
	Bin     string // 비우면 PATH 의 "claude"
	Model   string
	Timeout time.Duration

	mu sync.Mutex // 한 번에 한 호출만. 구독 한도와 PC 부하를 아낀다.
}

func NewClaudeCode(bin, model string, timeout time.Duration) *ClaudeCode {
	return &ClaudeCode{Bin: bin, Model: model, Timeout: timeout}
}

func (c *ClaudeCode) Name() string { return "Claude Code 구독 (" + c.Model + ")" }

type cliOutput struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	StopReason       string          `json:"stop_reason"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func (c *ClaudeCode) Generate(ctx context.Context, r Request) (json.RawMessage, error) {
	if r.Schema == nil {
		return nil, errors.New("스키마가 필요합니다")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	schema, err := json.Marshal(r.Schema)
	if err != nil {
		return nil, err
	}
	// 작업 폴더를 빈 임시 폴더로 두어 프로젝트의 CLAUDE.md, 설정, 도구가 섞이지 않게 한다.
	dir, err := os.MkdirTemp("", "minwon-llm-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	args := []string{
		"-p",
		"--output-format", "json",
		"--json-schema", string(schema),
		"--system-prompt", r.System,
		"--tools", "",
		"--no-session-persistence",
		"--disable-slash-commands",
		// 사용자 PC 에 설정된 MCP 서버를 호출마다 띄우지 않는다.
		"--strict-mcp-config",
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	// 시간 초과로 claude 를 끊어도 자식 프로세스가 출력 파이프를 쥐고 있을 수 있어, 기다리는 시간에 상한을 둔다.
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = strings.NewReader(r.User)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("claude 호출이 제한 시간 안에 끝나지 않았습니다: %w", ctx.Err())
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("claude 실행 파일을 찾지 못했습니다(설정의 claude 경로를 확인하세요): %w", err)
		}
		if runtime.GOOS == "windows" && strings.Contains(err.Error(), "batch file") {
			return nil, errors.New("claude 가 .cmd 실행 파일이라 인자를 안전하게 넘길 수 없습니다. " +
				"공식 설치 프로그램으로 설치한 claude.exe 의 경로를 설정의 'claude 실행 파일 경로'에 입력하세요")
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("claude 실행 실패: %v: %s", err, truncate(msg, 400))
	}
	return parseCLIOutput(stdout.Bytes())
}

func parseCLIOutput(b []byte) (json.RawMessage, error) {
	b = bytes.TrimSpace(b)
	var out cliOutput
	if err := json.Unmarshal(b, &out); err != nil {
		// 앞에 경고 줄이 붙은 경우를 대비해 마지막 JSON 줄만 다시 시도한다.
		lines := bytes.Split(b, []byte("\n"))
		ok := false
		for i := len(lines) - 1; i >= 0; i-- {
			if bytes.HasPrefix(bytes.TrimSpace(lines[i]), []byte("{")) &&
				json.Unmarshal(lines[i], &out) == nil {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("claude 출력을 해석하지 못했습니다: %s", truncate(string(b), 300))
		}
	}
	if out.StopReason == "refusal" {
		return nil, ErrRefusal
	}
	if out.IsError {
		return nil, fmt.Errorf("claude 가 오류를 돌려줬습니다(%s): %s", out.Subtype, truncate(out.Result, 400))
	}
	if so := bytes.TrimSpace(out.StructuredOutput); len(so) > 0 && !bytes.Equal(so, []byte("null")) {
		return json.RawMessage(so), nil
	}
	return ExtractJSON(out.Result)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
