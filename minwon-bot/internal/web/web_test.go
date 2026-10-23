package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/llm"
	"minwon-bot/internal/minwon"
	"minwon-bot/internal/store"
)

type env struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	st     *store.Store
	client *http.Client
	saved  []config.Config
	mu     sync.Mutex
	calls  []llm.Request
	// 가짜 AI 가 다음에 돌려줄 값
	draft    minwon.DraftResult
	eval     minwon.EvalResult
	followup minwon.DraftResult
	style    minwon.StyleProfile
	now      time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, st: st, now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)}
	e.draft = minwon.DraftResult{
		Title: "휴가 기준 문의", Body: "휴가 반려 기준이 궁금합니다.\n1. 기준을 알려 주세요.", Asks: []string{"기준을 알려 주세요."},
		Visibility: "private", VisibilityReason: "부대 정보",
	}
	e.followup = minwon.DraftResult{
		Title: "재문의", Body: "답변에서 빠진 점을 다시 여쭙니다.\n1. 구체적 규정 조항을 알려 주세요.", Asks: []string{"구체적 규정 조항을 알려 주세요."},
		Visibility: "private", VisibilityReason: "x",
	}
	e.eval = minwon.EvalResult{
		Items: []minwon.ItemEval{{Verdict: "evasive", Reason: "일반론", Evidence: "규정에 따라 처리", Confidence: "high"}},
		Score: 25, Summary: "일반론만 답함", Recommendation: "follow_up", RecommendationReason: "구체성 부족",
	}
	e.style = minwon.StyleProfile{Summary: "짧고 건조하게 쓴다.", Tone: "건조"}
	fake := llm.Func(func(ctx context.Context, r llm.Request) (json.RawMessage, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.calls = append(e.calls, r)
		var v any
		switch {
		case strings.Contains(r.System, "문체를 분석"):
			v = e.style
		case strings.Contains(r.System, "점검하는 보조자"):
			v = e.eval
		case strings.Contains(r.System, "재민원의 초안"):
			v = e.followup
		case strings.Contains(r.System, "작성을 돕는 보조자"):
			v = e.draft
		default:
			v = map[string]bool{"ok": true}
		}
		b, _ := json.Marshal(v)
		return b, nil
	})
	srv, err := New(Deps{
		Store: st, Config: config.Default(),
		SaveConfig: func(c config.Config) error { e.saved = append(e.saved, c); return nil },
		NewLLM:     func(config.Config) llm.LLM { return fake },
		Now:        func() time.Time { return e.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	e.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

func (e *env) do(method, path string, body any, headers map[string]string) (*http.Response, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *env) get(path string) (int, string) {
	resp, body := e.do("GET", path, nil, nil)
	return resp.StatusCode, body
}

// post 는 CSRF 토큰을 붙여 JSON 을 보내고 상태 코드와 응답을 map 으로 돌려준다.
func (e *env) post(path string, body any) (int, map[string]any) {
	e.t.Helper()
	resp, raw := e.do("POST", path, body, map[string]string{"X-CSRF-Token": e.srv.csrf})
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	return resp.StatusCode, m
}

func (e *env) mustOK(path string, body any) map[string]any {
	e.t.Helper()
	code, m := e.post(path, body)
	if code != 200 {
		e.t.Fatalf("POST %s = %d %v", path, code, m)
	}
	return m
}

func (e *env) createCase(facts string) int64 {
	e.t.Helper()
	form := url.Values{"csrf": {e.srv.csrf}, "title": {"휴가 반려"}, "agency": {"국방부"}, "facts": {facts}, "desired": {"기준 공개"}}
	req, _ := http.NewRequest("POST", e.ts.URL+"/cases", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/cases/") {
		e.t.Fatalf("사안 생성 실패: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	cases, _ := e.st.ListCases(context.Background())
	return cases[0].ID
}

func (e *env) lastCall() llm.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[len(e.calls)-1]
}

// ---------------------------------------------------------------- 보안

func TestSecurityHeadersHostAndCSRF(t *testing.T) {
	e := newEnv(t)

	resp, body := e.do("GET", "/", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `name="csrf" content="`+e.srv.csrf+`"`) {
		t.Fatalf("첫 화면과 CSRF 메타 태그: %d", resp.StatusCode)
	}
	for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer"} {
		if resp.Header.Get(k) != want {
			t.Errorf("%s = %q", k, resp.Header.Get(k))
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP 가 엄격해야 함: %q", csp)
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, " onclick=") {
		t.Error("인라인 스크립트가 있으면 안 됨")
	}
	// 엄격한 CSP(style-src 'self')에서는 인라인 style 속성이 무시되므로 쓰지 않는다.
	for _, p := range []string{"/", "/cases/new", "/settings"} {
		if _, b := e.get(p); strings.Contains(b, ` style="`) {
			t.Errorf("%s: 인라인 style 속성이 있음", p)
		}
	}

	// 다른 호스트 이름으로 들어오면(DNS rebinding) 거절
	req, _ := http.NewRequest("GET", e.ts.URL+"/", nil)
	req.Host = "evil.example.com"
	r2, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusForbidden {
		t.Errorf("외부 호스트는 403 이어야 함: %d", r2.StatusCode)
	}
	for _, host := range []string{"localhost:8745", "127.0.0.1:1", "[::1]:9"} {
		if !loopbackHost(host) {
			t.Errorf("%s 는 허용", host)
		}
	}

	// 토큰 없는 POST 는 거절
	resp, _ = e.do("POST", "/api/style/reset", map[string]any{}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF 토큰이 없으면 403: %d", resp.StatusCode)
	}
	resp, _ = e.do("POST", "/api/style/reset", map[string]any{}, map[string]string{"X-CSRF-Token": "wrong"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("틀린 토큰도 403: %d", resp.StatusCode)
	}

	code, _ := e.get("/static/app.js")
	if code != 200 {
		t.Errorf("정적 파일: %d", code)
	}
	if code, _ := e.get("/cases/999"); code != 404 {
		t.Errorf("없는 사안은 404: %d", code)
	}
}

// ---------------------------------------------------------------- 전체 흐름

func TestFullFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cid := e.createCase("제7사단 소속이고 010-1234-5678 로 연락됩니다. 휴가가 반려되었습니다.")
	cp := "/cases/" + itoa(cid)

	if code, body := e.get(cp); code != 200 || !strings.Contains(body, "초안 작성") {
		t.Fatalf("초안 작성 버튼이 보여야 함: %d", code)
	}

	// 1) 되묻기: 초안이 만들어지지 않는다.
	e.draft.NeedsInfo, e.draft.Questions = true, []string{"언제 있었던 일인가요?"}
	m := e.mustOK("/api/cases/"+itoa(cid)+"/draft", map[string]any{})
	if m["needs_info"] != true {
		t.Fatalf("되묻기여야 함: %v", m)
	}
	if subs, _ := e.st.ListSubmissions(ctx, cid); len(subs) != 0 {
		t.Fatal("되묻기에서는 제출 행이 생기면 안 됨")
	}
	// 개인정보가 AI 에 나가지 않았는지
	if call := e.lastCall(); strings.Contains(call.User, "010-1234-5678") {
		t.Fatalf("AI 에 전화번호가 나감:\n%s", call.User)
	}

	// 2) 답을 달아 초안 작성
	e.draft.NeedsInfo, e.draft.Questions = false, nil
	e.mustOK("/api/cases/"+itoa(cid)+"/draft", map[string]any{"extra": []map[string]string{{"question": "언제 있었던 일인가요?", "answer": "9월 첫째 주"}}})
	c, _ := e.st.GetCase(ctx, cid)
	if !strings.Contains(c.Facts, "9월 첫째 주") {
		t.Fatalf("되묻기 답이 사실관계에 남아야 함: %q", c.Facts)
	}
	subs, _ := e.st.ListSubmissions(ctx, cid)
	if len(subs) != 1 || subs[0].Status != "draft" || subs[0].Round != 1 {
		t.Fatalf("초안 1건이어야 함: %+v", subs)
	}
	sid := subs[0].ID
	if code, _ := e.post("/api/cases/"+itoa(cid)+"/draft", map[string]any{}); code != http.StatusConflict {
		t.Errorf("이미 초안이 있으면 409: %d", code)
	}

	// 3) 화면에 초안이 보이고, 저장하면 점검 결과가 온다
	_, page := e.get(cp)
	for _, want := range []string{`id="body-` + itoa(sid) + `"`, "휴가 반려 기준이 궁금합니다.", "공개 여부 권고", "비공개"} {
		if !strings.Contains(page, want) {
			t.Errorf("초안 화면에 %q 없음", want)
		}
	}
	if strings.Contains(page, ` style="`) {
		t.Error("사안 화면에 인라인 style 속성이 있음")
	}
	m = e.mustOK("/api/submissions/"+itoa(sid)+"/save", map[string]any{
		"title": "휴가 기준 문의", "body": "수정한 본문. 제 번호는 010-9999-8888 입니다.", "asks": []string{"기준을 알려 주세요.", " "},
	})
	fs, _ := m["findings"].([]any)
	if len(fs) != 1 || fs[0].(map[string]any)["code"] != "pii" {
		t.Fatalf("본문의 전화번호를 경고해야 함: %v", m["findings"])
	}

	// 4) 제출 기록: 최종본이 AI 초안과 다르면 문체 학습 재료로 남는다
	code, m := e.post("/api/submissions/"+itoa(sid)+"/submitted", map[string]any{
		"receipt_no": "  ", "title": "t", "body": "b", "asks": []string{"a"},
	})
	if code != 400 {
		t.Fatalf("접수번호가 비면 400: %d %v", code, m)
	}
	e.mustOK("/api/submissions/"+itoa(sid)+"/submitted", map[string]any{
		"receipt_no": "2AA-2610-0001", "title": "휴가 기준 문의", "body": "수정한 본문입니다. 기준을 알려 주세요.", "asks": []string{"기준을 알려 주세요."},
	})
	sub, _ := e.st.GetSubmission(ctx, sid)
	if sub.Status != "submitted" || sub.ReceiptNo != "2AA-2610-0001" || sub.Body != "수정한 본문입니다. 기준을 알려 주세요." {
		t.Fatalf("제출 상태 불일치: %+v", sub)
	}
	if edits, _ := e.st.ListStyleSamples(ctx, "edit", 5); len(edits) != 1 || edits[0].Original != e.draft.Body {
		t.Fatalf("고친 초안이 문체 재료로 남아야 함: %+v", edits)
	}
	if code, _ := e.post("/api/submissions/"+itoa(sid)+"/save", map[string]any{"title": "x", "body": "x"}); code != 404 {
		t.Errorf("제출 후 본문 수정은 막혀야 함: %d", code)
	}

	// 5) 답변 기록, 평가
	e.e2eAnswerAndEvaluate(cid, sid, cp)

	// 6) 재민원 초안 → 같은 사안에 초안이 있으면 또 못 만든다
	ans, _ := e.st.ListAnswers(ctx, sid)
	e.mustOK("/api/answers/"+itoa(ans[0].ID)+"/followup", map[string]any{})
	subs, _ = e.st.ListSubmissions(ctx, cid)
	if len(subs) != 2 || subs[1].Round != 2 || subs[1].Kind != "followup" || subs[1].Status != "draft" {
		t.Fatalf("재민원 초안(2회차)이어야 함: %+v", subs)
	}
	fu := e.lastCall()
	if !strings.Contains(fu.User, "2AA-2610-0001") || !strings.Contains(fu.User, "<새_회차>2</새_회차>") {
		t.Errorf("재민원 요청에 접수번호와 회차가 있어야 함:\n%s", fu.User)
	}
	if code, _ := e.post("/api/answers/"+itoa(ans[0].ID)+"/followup", map[string]any{}); code != http.StatusConflict {
		t.Errorf("초안이 있으면 재민원 생성은 409: %d", code)
	}
	_, page = e.get(cp)
	if !strings.Contains(page, "2회차 재민원") || !strings.Contains(page, "구체적 규정 조항을 알려 주세요.") {
		t.Error("재민원 초안이 화면에 보여야 함")
	}

	// 7) 종결
	if code, _ := e.post("/api/cases/"+itoa(cid)+"/close", map[string]any{"reason": " "}); code != 400 {
		t.Errorf("사유 없는 종결은 400: %d", code)
	}
	e.mustOK("/api/cases/"+itoa(cid)+"/close", map[string]any{"reason": "충분히 답변받음"})
	c, _ = e.st.GetCase(ctx, cid)
	if c.Status != "closed" {
		t.Fatal("종결되어야 함")
	}
	if code, _ := e.post("/api/cases/"+itoa(cid)+"/draft", map[string]any{}); code != http.StatusConflict {
		t.Errorf("종결된 사안은 409: %d", code)
	}
	_, page = e.get(cp)
	if !strings.Contains(page, "종결 사유: 충분히 답변받음") || !strings.Contains(page, "활동 기록") {
		t.Error("종결 사유와 활동 기록이 보여야 함")
	}
	if strings.Contains(page, ` style="`) {
		t.Error("종결된 사안 화면에 인라인 style 속성이 있음")
	}
}

func (e *env) e2eAnswerAndEvaluate(cid, sid int64, cp string) {
	e.t.Helper()
	ctx := context.Background()
	if code, _ := e.post("/api/submissions/"+itoa(sid)+"/answers", map[string]any{"body": " "}); code != 400 {
		e.t.Errorf("빈 답변은 400: %d", code)
	}
	e.mustOK("/api/submissions/"+itoa(sid)+"/answers", map[string]any{"body": "관련 규정에 따라 처리되었습니다. 담당자 홍길동", "dept": "인사복지실"})
	ans, _ := e.st.ListAnswers(ctx, sid)
	if len(ans) != 1 {
		e.t.Fatal("답변 1건이어야 함")
	}
	aid := itoa(ans[0].ID)
	// 평가 전에는 재민원을 만들 수 없다
	if code, _ := e.post("/api/answers/"+aid+"/followup", map[string]any{}); code != http.StatusConflict {
		e.t.Errorf("평가 전 재민원은 409: %d", code)
	}
	e.mustOK("/api/answers/"+aid+"/evaluate", map[string]any{})
	_, page := e.get(cp)
	for _, want := range []string{"일반론만 답함", "회피", "재민원 권고", `data-action="followup"`, "구체성 부족"} {
		if !strings.Contains(page, want) {
			e.t.Errorf("평가 화면에 %q 없음", want)
		}
	}
	if !strings.Contains(page, "기준을 알려 주세요.") {
		e.t.Error("평가 항목에 질의 원문이 보여야 함")
	}
}

func TestPageEscapesUserAndModelText(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	evil := `<script>alert(1)</script><img src=x onerror=alert(2)>`
	cid, _ := e.st.CreateCase(ctx, store.Case{Title: evil, Facts: evil, Desired: evil})
	e.draft.Body = evil
	e.draft.Warnings = []string{evil}
	e.mustOK("/api/cases/"+itoa(cid)+"/draft", map[string]any{})
	for _, p := range []string{"/", "/cases/" + itoa(cid)} {
		_, body := e.get(p)
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
			t.Errorf("%s: 사용자·모델 글이 이스케이프되지 않음", p)
		}
	}
}

func TestFollowUpRefusedWhenAllSufficient(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cid, _ := e.st.CreateCase(ctx, store.Case{Title: "t", Facts: "f"})
	sub, _ := e.st.CreateSubmission(ctx, cid, "initial", "", "t", "b", `["질의"]`)
	e.st.MarkSubmitted(ctx, sub.ID, "N1")
	ans, _ := e.st.CreateAnswer(ctx, sub.ID, "답변", "")
	e.eval.Items = []minwon.ItemEval{{Verdict: "sufficient", Reason: "ok", Confidence: "high"}}
	e.eval.Recommendation = "close"
	e.mustOK("/api/answers/"+itoa(ans.ID)+"/evaluate", map[string]any{})
	code, m := e.post("/api/answers/"+itoa(ans.ID)+"/followup", map[string]any{})
	if code != http.StatusConflict || !strings.Contains(m["error"].(string), "필요한 항목이 없습니다") {
		t.Fatalf("충분한 답변에는 재민원을 만들지 않아야 함: %d %v", code, m)
	}
}

// ---------------------------------------------------------------- 설정, 문체

func TestSettings(t *testing.T) {
	e := newEnv(t)
	cfg := config.Default()
	cfg.MaskTerms = []string{" 이름:홍길동 ", "이름:홍길동", ""}
	e.mustOK("/api/settings", cfg)
	if len(e.saved) != 1 || len(e.saved[0].MaskTerms) != 1 || e.saved[0].MaskTerms[0] != "이름:홍길동" {
		t.Fatalf("낱말은 다듬고 중복을 없애야 함: %+v", e.saved)
	}
	if got := e.srv.config().MaskTerms; len(got) != 1 {
		t.Errorf("메모리 설정도 갱신: %v", got)
	}
	bad := config.Default()
	bad.Limits.DailyMax = 0
	if code, m := e.post("/api/settings", bad); code != 400 || !strings.Contains(m["error"].(string), "daily_max") {
		t.Errorf("잘못된 설정은 400: %d %v", code, m)
	}
	bad = config.Default()
	bad.LLM.Backend = "api" // 모델이 별칭 "opus" 인 채로
	if code, _ := e.post("/api/settings", bad); code != 400 {
		t.Errorf("API 방식에 별칭 모델은 400: %d", code)
	}
	if len(e.saved) != 1 {
		t.Error("검증에 실패한 설정은 저장하면 안 됨")
	}
	if code, body := e.get("/settings"); code != 200 || !strings.Contains(body, "이름:홍길동") {
		t.Errorf("설정 화면: %d", code)
	}
	m := e.mustOK("/api/llm/test", map[string]any{})
	if m["ok"] != true {
		t.Errorf("연결 테스트: %v", m)
	}
}

func TestStyleLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if code, _ := e.post("/api/style/extract", map[string]any{}); code == 200 {
		t.Fatal("글이 없으면 학습이 실패해야 함")
	}
	if code, _ := e.post("/api/style/samples", map[string]any{"text": "  "}); code != 400 {
		t.Errorf("빈 글은 400: %d", code)
	}
	e.mustOK("/api/style/samples", map[string]any{"text": "안녕하세요. 문의드립니다."})
	e.mustOK("/api/style/extract", map[string]any{})
	if got := e.srv.loadStyle(ctx); got == nil || got.Tone != "건조" {
		t.Fatalf("학습된 프로필이 저장되어야 함: %+v", got)
	}
	if _, body := e.get("/settings"); !strings.Contains(body, "짧고 건조하게 쓴다.") {
		t.Error("설정 화면에 프로필이 보여야 함")
	}
	// 학습된 문체는 다음 초안 요청에 들어간다
	cid, _ := e.st.CreateCase(ctx, store.Case{Title: "t", Facts: "사실"})
	e.mustOK("/api/cases/"+itoa(cid)+"/draft", map[string]any{})
	if call := e.lastCall(); !strings.Contains(call.User, "<문체_프로필>") || !strings.Contains(call.User, "짧고 건조하게 쓴다.") {
		t.Errorf("초안 요청에 문체가 들어가야 함:\n%s", call.User)
	}
	samples, _ := e.st.ListStyleSamples(ctx, "", 10)
	e.mustOK("/api/style/samples/"+itoa(samples[0].ID)+"/delete", map[string]any{})
	if left, _ := e.st.ListStyleSamples(ctx, "", 10); len(left) != 0 {
		t.Error("삭제되어야 함")
	}
	e.mustOK("/api/style/reset", map[string]any{})
	if e.srv.loadStyle(ctx) != nil {
		t.Error("프로필이 지워져야 함")
	}
}

func TestGuardUsesCrossCaseLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// 다른 사안에서 방금 3건 제출 → 새 초안은 하루 한도·간격 경고
	other, _ := e.st.CreateCase(ctx, store.Case{Title: "다른 사안", Facts: "f"})
	e.st.SetClock(func() time.Time { return e.now.Add(-2 * time.Minute) })
	for i := 0; i < 3; i++ {
		s, _ := e.st.CreateSubmission(ctx, other, "initial", "", "t", "b", "[]")
		e.st.MarkSubmitted(ctx, s.ID, "N")
	}
	cid, _ := e.st.CreateCase(ctx, store.Case{Title: "t", Facts: "사실"})
	e.mustOK("/api/cases/"+itoa(cid)+"/draft", map[string]any{})
	_, page := e.get("/cases/" + itoa(cid))
	if !strings.Contains(page, "하루 한도 3건") || !strings.Contains(page, "최소 간격") {
		t.Errorf("다른 사안의 제출도 한도에 포함되어야 함")
	}
}
