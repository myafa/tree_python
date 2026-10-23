package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestExtractJSON(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`:                 true,
		"```json\n{\"a\":1}\n```": true,
		"```\n{\"a\":1}\n```":     true,
		"결과입니다.\n{\"a\":1}\n끝":    true,
		"JSON 이 없습니다":             false,
		`{"a":`:                   false,
	}
	for in, ok := range cases {
		got, err := ExtractJSON(in)
		if ok && (err != nil || !json.Valid(got)) {
			t.Errorf("%q: 실패 %v", in, err)
		}
		if !ok && err == nil {
			t.Errorf("%q: 통과하면 안 됨", in)
		}
	}
}

func TestSchemaHelpers(t *testing.T) {
	s := Obj(map[string]any{"b": Str(), "a": Arr(Int())})
	req := s["required"].([]string)
	if len(req) != 2 || req[0] != "a" || req[1] != "b" || s["additionalProperties"] != false {
		t.Fatalf("스키마 도우미 불일치: %v", s)
	}
}

func TestParseCLIOutput(t *testing.T) {
	good := `{"type":"result","is_error":false,"stop_reason":"tool_use","result":"{\"x\":2}","structured_output":{"x":1}}`
	if raw, err := parseCLIOutput([]byte(good)); err != nil || string(raw) != `{"x":1}` {
		t.Fatalf("structured_output 우선이어야 함: %s %v", raw, err)
	}
	noStruct := `{"is_error":false,"result":"` + "```json\\n{\\\"x\\\":3}\\n```" + `","structured_output":null}`
	if raw, err := parseCLIOutput([]byte(noStruct)); err != nil || string(raw) != `{"x":3}` {
		t.Fatalf("result 에서 JSON 추출해야 함: %s %v", raw, err)
	}
	withNoise := "경고: 무언가\n" + good
	if raw, err := parseCLIOutput([]byte(withNoise)); err != nil || string(raw) != `{"x":1}` {
		t.Fatalf("앞 줄 잡음 허용: %s %v", raw, err)
	}
	if _, err := parseCLIOutput([]byte(`{"is_error":true,"subtype":"error_during_execution","result":"로그인 필요"}`)); err == nil ||
		!strings.Contains(err.Error(), "로그인 필요") {
		t.Fatalf("오류 전달 실패: %v", err)
	}
	if _, err := parseCLIOutput([]byte(`{"stop_reason":"refusal","result":""}`)); !errors.Is(err, ErrRefusal) {
		t.Fatalf("거절은 ErrRefusal: %v", err)
	}
	if _, err := parseCLIOutput([]byte(`not json`)); err == nil {
		t.Fatal("해석 불가는 오류")
	}
}

// 가짜 claude 스크립트로 인자, 표준입력, 작업 폴더를 확인한다.
func TestClaudeCodeGenerateUsesIsolatedHeadlessCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh 스크립트 사용")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stdinFile := filepath.Join(dir, "stdin.txt")
	cwdFile := filepath.Join(dir, "cwd.txt")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + argsFile + `"
cat > "` + stdinFile + `"
pwd > "` + cwdFile + `"
echo '{"is_error":false,"result":"","structured_output":{"ok":true}}'
`
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewClaudeCode(bin, "opus", 10*time.Second)
	raw, err := c.Generate(context.Background(), Request{
		System: "시스템 프롬프트",
		User:   "사용자 입력 {{PII_1}}",
		Schema: Obj(map[string]any{"ok": Bool()}),
	})
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("결과 불일치: %s %v", raw, err)
	}
	args, _ := os.ReadFile(argsFile)
	a := string(args)
	for _, want := range []string{"-p\n", "--output-format\njson\n", "--json-schema\n", "--system-prompt\n시스템 프롬프트\n",
		"--tools\n\n", "--no-session-persistence\n", "--disable-slash-commands\n", "--strict-mcp-config\n", "--model\nopus\n"} {
		if !strings.Contains(a, want) {
			t.Errorf("인자에 %q 없음:\n%s", want, a)
		}
	}
	in, _ := os.ReadFile(stdinFile)
	if string(in) != "사용자 입력 {{PII_1}}" {
		t.Errorf("표준입력 불일치: %q", in)
	}
	cwd, _ := os.ReadFile(cwdFile)
	if strings.TrimSpace(string(cwd)) == "" || strings.Contains(string(cwd), "minwon-bot/internal") {
		t.Errorf("작업 폴더가 임시 폴더가 아님: %q", cwd)
	}
}

func TestClaudeCodeMissingBinary(t *testing.T) {
	c := NewClaudeCode("/no/such/claude-binary", "opus", time.Second)
	_, err := c.Generate(context.Background(), Request{System: "s", User: "u", Schema: Obj(map[string]any{})})
	if err == nil || !strings.Contains(err.Error(), "찾지 못했습니다") {
		t.Fatalf("친절한 안내 오류여야 함: %v", err)
	}
}

func TestClaudeCodeTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh 스크립트 사용")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	os.WriteFile(bin, []byte("#!/bin/sh\nsleep 5\n"), 0o755)
	c := NewClaudeCode(bin, "opus", 300*time.Millisecond)
	start := time.Now()
	_, err := c.Generate(context.Background(), Request{System: "s", User: "u", Schema: Obj(map[string]any{})})
	if err == nil || !strings.Contains(err.Error(), "제한 시간") {
		t.Fatalf("제한 시간 오류여야 함: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("제한 시간이 지켜지지 않음")
	}
}

const apiOK = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
"content":[{"type":"thinking","thinking":"","signature":"x"},{"type":"text","text":"{\"ok\":true}"}],
"stop_reason":"end_turn","stop_details":null,"stop_sequence":null,
"usage":{"input_tokens":10,"output_tokens":5}}`

func apiServer(t *testing.T, status int, body string, capture *map[string]any, hdr *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			json.Unmarshal(b, capture)
		}
		if hdr != nil {
			*hdr = r.Header.Clone()
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
}

func TestAPIGenerateSendsSchemaEffortAndFallback(t *testing.T) {
	var req map[string]any
	var hdr http.Header
	srv := apiServer(t, 200, apiOK, &req, &hdr)
	defer srv.Close()

	a := NewAPI("claude-opus-5-5", "high", 10*time.Second,
		option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0))
	raw, err := a.Generate(context.Background(), Request{
		System: "시스템", User: "입력", Schema: Obj(map[string]any{"ok": Bool()}),
	})
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("결과 불일치: %s %v", raw, err)
	}
	if req["model"] != "claude-opus-5-5" {
		t.Errorf("모델 불일치: %v", req["model"])
	}
	oc, _ := req["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Errorf("effort 불일치: %v", oc)
	}
	format, _ := oc["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("구조화 출력 스키마가 없음: %v", format)
	}
	if req["fallbacks"] != "default" {
		t.Errorf("fallbacks 가 default 여야 함: %v", req["fallbacks"])
	}
	if !strings.Contains(hdr.Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("fallback 베타 헤더 없음: %q", hdr.Get("anthropic-beta"))
	}
	if _, has := req["thinking"]; has {
		t.Errorf("Opus 5.5 는 thinking 을 보내지 않아야 함: %v", req["thinking"])
	}
	if _, has := req["temperature"]; has {
		t.Error("temperature 를 보내면 안 됨")
	}
}

func TestAPIOtherModelNoFallback(t *testing.T) {
	var req map[string]any
	srv := apiServer(t, 200, apiOK, &req, nil)
	defer srv.Close()
	a := NewAPI("claude-haiku-5-5", "low", time.Second,
		option.WithBaseURL(srv.URL), option.WithAPIKey("k"), option.WithMaxRetries(0))
	if _, err := a.Generate(context.Background(), Request{System: "s", User: "u", Schema: Obj(map[string]any{"ok": Bool()})}); err != nil {
		t.Fatal(err)
	}
	if _, has := req["fallbacks"]; has {
		t.Errorf("fallback 미지원 모델에는 보내지 않아야 함: %v", req["fallbacks"])
	}
}

func TestAPIRefusalAndAuthErrors(t *testing.T) {
	refusal := strings.Replace(apiOK, `"stop_reason":"end_turn"`, `"stop_reason":"refusal"`, 1)
	srv := apiServer(t, 200, refusal, nil, nil)
	a := NewAPI("claude-opus-5-5", "high", time.Second,
		option.WithBaseURL(srv.URL), option.WithAPIKey("k"), option.WithMaxRetries(0))
	if _, err := a.Generate(context.Background(), Request{System: "s", User: "u", Schema: Obj(map[string]any{})}); !errors.Is(err, ErrRefusal) {
		t.Errorf("거절은 ErrRefusal: %v", err)
	}
	srv.Close()

	srv = apiServer(t, 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, nil, nil)
	defer srv.Close()
	a = NewAPI("claude-opus-5-5", "high", time.Second,
		option.WithBaseURL(srv.URL), option.WithAPIKey("bad"), option.WithMaxRetries(0))
	_, err := a.Generate(context.Background(), Request{System: "s", User: "u", Schema: Obj(map[string]any{})})
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("인증 오류 안내가 있어야 함: %v", err)
	}
}

func TestDecode(t *testing.T) {
	type T struct {
		A string `json:"a"`
	}
	v, err := Decode[T](json.RawMessage(`{"a":"x","extra":1}`))
	if err != nil || v.A != "x" {
		t.Fatalf("해석 실패: %+v %v", v, err)
	}
	if _, err := Decode[T](json.RawMessage(`[1]`)); err == nil {
		t.Fatal("형식 불일치는 오류")
	}
}
