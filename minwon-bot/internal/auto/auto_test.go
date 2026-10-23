package auto

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/llm"
	"minwon-bot/internal/minwon"
	"minwon-bot/internal/portal"
	"minwon-bot/internal/report"
	"minwon-bot/internal/store"
)

type recorder struct {
	mu   sync.Mutex
	reps []report.Report
}

func (r *recorder) Name() string { return "기록" }
func (r *recorder) Send(_ context.Context, x report.Report) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reps = append(r.reps, x)
	return nil
}
func (r *recorder) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var k []string
	for _, x := range r.reps {
		k = append(k, x.Kind)
	}
	return k
}
func (r *recorder) count(kind string) int {
	n := 0
	for _, k := range r.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}
func (r *recorder) last() report.Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reps[len(r.reps)-1]
}

type harness struct {
	t      *testing.T
	st     *store.Store
	portal *portal.Fake // nil 이면 사이트 연동 없음
	rep    *recorder
	run    *Runner
	cfg    config.Config
	now    time.Time

	eval       minwon.EvalResult
	followup   minwon.DraftResult
	evalErr    error
	followErr  error
	evalCalls  int
	followCall int
	requests   []llm.Request
}

func newHarness(t *testing.T, withPortal bool) *harness {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, st: st, rep: &recorder{}, cfg: config.Default(),
		now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)}
	st.SetClock(func() time.Time { return h.now })
	if withPortal {
		h.portal = portal.NewFake()
	}
	h.eval = minwon.EvalResult{
		Items:          []minwon.ItemEval{{Verdict: "evasive", Reason: "일반론", Evidence: "", Confidence: "high"}},
		Score:          25,
		Summary:        "일반론만 답했습니다.",
		Recommendation: minwon.RecFollowUp, RecommendationReason: "구체성 부족", FollowUpFocus: []string{"반려 기준"},
	}
	h.followup = minwon.DraftResult{
		Title:      "휴가 반려 기준 재문의",
		Body:       "답변해 주셔서 감사합니다. 훈련을 이유로 반려할 때 적용한 훈령의 이름과 조항을 알려 주시기 바랍니다.",
		Asks:       []string{"적용한 훈령의 이름과 조항을 알려 주시기 바랍니다."},
		Visibility: "private", VisibilityReason: "부대 정보",
	}
	fake := llm.Func(func(ctx context.Context, r llm.Request) (json.RawMessage, error) {
		h.requests = append(h.requests, r)
		switch {
		case strings.Contains(r.System, "점검하는 보조자"):
			h.evalCalls++
			if h.evalErr != nil {
				return nil, h.evalErr
			}
			return json.Marshal(h.eval)
		case strings.Contains(r.System, "재민원의 초안"):
			h.followCall++
			if h.followErr != nil {
				return nil, h.followErr
			}
			return json.Marshal(h.followup)
		}
		return nil, errors.New("예상하지 못한 AI 호출")
	})
	d := Deps{
		Store:    st,
		Service:  func() *minwon.Service { return minwon.New(fake, nil) },
		Config:   func() config.Config { return h.cfg },
		Reporter: func() report.Reporter { return h.rep },
		Now:      func() time.Time { return h.now },
	}
	if h.portal != nil {
		d.Portal = h.portal
	}
	h.run = New(d)
	return h
}

// answeredCase 는 rounds 회차까지 제출·답변된 사안을 만들고, 마지막 회차의 미처리 답변을 돌려준다.
func (h *harness) answeredCase(rounds int) (store.Case, store.Answer) {
	h.t.Helper()
	ctx := context.Background()
	cid, err := h.st.CreateCase(ctx, store.Case{Title: "휴가 반려", Agency: "국방부", Facts: "사실", Desired: "기준 공개"})
	if err != nil {
		h.t.Fatal(err)
	}
	real := h.now
	h.st.SetClock(func() time.Time { return real.Add(-48 * time.Hour) }) // 한도에 걸리지 않게 오래전에 제출한 것으로
	var last store.Answer
	for i := 1; i <= rounds; i++ {
		kind := "initial"
		if i > 1 {
			kind = "followup"
		}
		sub, err := h.st.CreateSubmission(ctx, cid, kind, "", "휴가 기준 문의", "휴가 반려 기준이 궁금합니다. 기준을 알려 주세요.", `["기준을 알려 주세요."]`)
		if err != nil {
			h.t.Fatal(err)
		}
		h.st.MarkSubmitted(ctx, sub.ID, "R"+string(rune('0'+i)))
		last, _ = h.st.CreateAnswer(ctx, sub.ID, "관련 규정에 따라 처리되었습니다.", "인사복지실")
		if i < rounds {
			h.st.MarkAnswerProcessed(ctx, last.ID)
		}
	}
	h.st.SetClock(func() time.Time { return h.now })
	c, _ := h.st.GetCase(ctx, cid)
	return c, last
}

func (h *harness) process(a store.Answer) { h.run.ProcessAnswer(context.Background(), a) }

func (h *harness) caseNow(id int64) store.Case {
	c, err := h.st.GetCase(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) answerNow(id int64) store.Answer {
	a, err := h.st.GetAnswer(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func (h *harness) subs(caseID int64) []store.Submission {
	s, _ := h.st.ListSubmissions(context.Background(), caseID)
	return s
}

// ---------------------------------------------------------------- 결정 규칙

func TestDecide(t *testing.T) {
	cfg := config.Default()
	cases := []struct {
		name string
		rec  string
		next int
		hold string
		want action
		kind string
	}{
		{"충분 → 종결", minwon.RecClose, 2, "notify", actClose, kindSufficient},
		{"다른 경로 → 종결", minwon.RecOtherRoute, 2, "notify", actClose, kindOtherRoute},
		{"재민원(2회차) → 재민원", minwon.RecFollowUp, 2, "notify", actFollowUp, ""},
		{"재민원(3회차) → 한도 종결", minwon.RecFollowUp, 3, "notify", actClose, kindLimit},
		{"보류+notify → 보고만", minwon.RecHold, 2, "notify", actNotify, ""},
		{"보류+close → 종결", minwon.RecHold, 2, "close", actClose, kindHoldPolicy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Auto.HoldPolicy = tc.hold
			got := decide(cfg, tc.next, minwon.EvalResult{Recommendation: tc.rec, Score: 50})
			if got.act != tc.want || got.kind != tc.kind {
				t.Fatalf("decide = %+v", got)
			}
		})
	}
}

// ---------------------------------------------------------------- 종결

func TestCloseWhenSufficient(t *testing.T) {
	h := newHarness(t, true)
	h.eval.Recommendation = minwon.RecClose
	h.eval.Items = []minwon.ItemEval{{Verdict: "sufficient", Reason: "ok", Confidence: "high"}}
	h.eval.Score = 90
	c, ans := h.answeredCase(1)
	h.process(ans)

	got := h.caseNow(c.ID)
	if got.Status != "closed" || !strings.HasPrefix(got.ClosedReason, "(자동)") {
		t.Fatalf("자동 종결되어야 함: %+v", got)
	}
	if h.answerNow(ans.ID).ProcessedAt.IsZero() {
		t.Error("처리 표시가 되어야 함")
	}
	if h.followCall != 0 || h.portal.SubmittedCount() != 0 {
		t.Error("재민원을 만들거나 제출하면 안 됨")
	}
	r := h.rep.last()
	if r.Kind != report.KindClosed || !strings.Contains(r.Summary, kindSufficient) || !strings.Contains(r.Body, "다시 열기") {
		t.Fatalf("종결 보고: %+v", r)
	}
	if strings.Contains(r.Summary, "일반론") || strings.Contains(r.Title, "휴가") {
		t.Error("외부로 나갈 제목·요약에는 내용이 없어야 함")
	}
}

func TestOtherRouteClosesWithRoutes(t *testing.T) {
	h := newHarness(t, true)
	h.eval.Recommendation = minwon.RecOtherRoute
	h.eval.OtherRoutes = []string{"국민권익위원회 고충민원"}
	c, ans := h.answeredCase(1)
	h.process(ans)
	if h.caseNow(c.ID).Status != "closed" {
		t.Fatal("종결되어야 함")
	}
	if !strings.Contains(h.rep.last().Body, "국민권익위원회 고충민원") {
		t.Errorf("다른 경로가 보고에 있어야 함: %s", h.rep.last().Body)
	}
}

func TestRepeatLimitClosesWithoutCallingAIForDraft(t *testing.T) {
	h := newHarness(t, true)
	c, ans := h.answeredCase(2) // 2회차 답변이 미흡 → 다음은 3회차
	h.process(ans)
	if h.caseNow(c.ID).Status != "closed" {
		t.Fatal("한도에 도달하면 종결해야 함")
	}
	if h.followCall != 0 || h.portal.SubmittedCount() != 0 {
		t.Errorf("재민원을 만들거나 제출하면 안 됨: calls=%d", h.followCall)
	}
	if !strings.Contains(h.rep.last().Summary, kindLimit) {
		t.Errorf("한도 종결 보고: %+v", h.rep.last())
	}
}

func TestHoldPolicy(t *testing.T) {
	h := newHarness(t, true)
	h.eval.Recommendation = minwon.RecHold
	c, ans := h.answeredCase(1)
	h.process(ans)
	got := h.caseNow(c.ID)
	if got.Status != "open" || got.Attention == "" || h.rep.count(report.KindHold) != 1 {
		t.Fatalf("notify 는 열어 두고 보고만: %+v kinds=%v", got, h.rep.kinds())
	}
	if h.answerNow(ans.ID).ProcessedAt.IsZero() {
		t.Error("보류도 처리 표시가 되어야 같은 보고가 반복되지 않음")
	}

	h2 := newHarness(t, true)
	h2.cfg.Auto.HoldPolicy = config.HoldClose
	h2.eval.Recommendation = minwon.RecHold
	c2, ans2 := h2.answeredCase(1)
	h2.process(ans2)
	if h2.caseNow(c2.ID).Status != "closed" {
		t.Fatal("close 정책이면 종결해야 함")
	}
}

// ---------------------------------------------------------------- 재민원

func TestFollowUpSubmittedViaPortal(t *testing.T) {
	h := newHarness(t, true)
	c, ans := h.answeredCase(1)
	h.process(ans)

	if h.portal.SubmittedCount() != 1 {
		t.Fatalf("사이트에 1건 제출되어야 함: %d", h.portal.SubmittedCount())
	}
	form := h.portal.Submitted[0]
	if form.Title != h.followup.Title || form.Agency != "국방부" || !form.Private {
		t.Errorf("제출 내용 불일치: %+v", form)
	}
	subs := h.subs(c.ID)
	if len(subs) != 2 || subs[1].Round != 2 || subs[1].Kind != "followup" || subs[1].Status != "submitted" || subs[1].ReceiptNo != "FAKE-0001" {
		t.Fatalf("2회차 재민원이 제출·기록되어야 함: %+v", subs)
	}
	if h.answerNow(ans.ID).ProcessedAt.IsZero() || h.caseNow(c.ID).Attention != "" {
		t.Error("처리 표시와 주의 해제")
	}
	r := h.rep.last()
	if r.Kind != report.KindFollowUpSubmitted || !strings.Contains(r.Summary, "FAKE-0001") || !strings.Contains(r.Body, h.followup.Body) {
		t.Fatalf("제출 보고: %+v", r)
	}
	// 두 번 처리해도 다시 제출하지 않는다.
	h.process(ans)
	if h.portal.SubmittedCount() != 1 || h.evalCalls != 1 {
		t.Error("이미 처리한 답변은 건드리지 않아야 함")
	}
}

func TestFollowUpWithoutPortalLeavesDraft(t *testing.T) {
	h := newHarness(t, false)
	c, ans := h.answeredCase(1)
	h.process(ans)
	subs := h.subs(c.ID)
	if len(subs) != 2 || subs[1].Status != "draft" || subs[1].Kind != "followup" {
		t.Fatalf("초안만 남아야 함: %+v", subs)
	}
	r := h.rep.last()
	if r.Kind != report.KindFollowUpDrafted || !strings.Contains(r.Body, h.followup.Body) {
		t.Fatalf("초안 보고에 본문이 있어야 함(직접 제출용): %+v", r)
	}
	if h.caseNow(c.ID).Attention == "" || h.answerNow(ans.ID).ProcessedAt.IsZero() {
		t.Error("직접 제출하라는 주의 표시와 처리 표시")
	}
}

func TestNoPortalIgnoresSubmitLimits(t *testing.T) {
	// 사이트 제출이 없으면 하루 한도는 의미가 없다. 초안은 만들어야 한다.
	h := newHarness(t, false)
	h.cfg.Limits.DailyMax = 1
	other, _ := h.st.CreateCase(context.Background(), store.Case{Title: "다른"})
	s, _ := h.st.CreateSubmission(context.Background(), other, "initial", "", "t", "b", "[]")
	h.st.MarkSubmitted(context.Background(), s.ID, "X")
	_, ans := h.answeredCase(1)
	h.process(ans)
	if h.followCall != 1 || h.rep.count(report.KindFollowUpDrafted) != 1 {
		t.Fatalf("연동이 없으면 한도와 무관하게 초안을 만들어야 함: calls=%d kinds=%v", h.followCall, h.rep.kinds())
	}
}

func TestDeferOnDailyLimitThenResume(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	// 다른 사안에서 최근 3건을 제출함 → 하루 한도 도달
	other, _ := h.st.CreateCase(ctx, store.Case{Title: "다른 사안"})
	h.st.SetClock(func() time.Time { return h.now.Add(-time.Hour) })
	for i := 0; i < 3; i++ {
		s, _ := h.st.CreateSubmission(ctx, other, "initial", "", "t", "b", "[]")
		h.st.MarkSubmitted(ctx, s.ID, "O")
	}
	h.st.SetClock(func() time.Time { return h.now })
	c, ans := h.answeredCase(1)

	h.process(ans)
	if h.followCall != 0 || h.portal.SubmittedCount() != 0 {
		t.Fatal("한도에 걸리면 AI 도 부르지 않고 제출하지도 않아야 함")
	}
	if h.answerNow(ans.ID).ProcessedAt.IsZero() == false {
		t.Fatal("미뤘으므로 처리 표시를 하면 안 됨")
	}
	if h.caseNow(c.ID).Status != "open" || h.rep.count(report.KindAttention) != 1 {
		t.Fatalf("열어 두고 한 번만 보고: kinds=%v", h.rep.kinds())
	}
	// 같은 상태에서 다시 점검해도 보고를 반복하지 않는다.
	h.process(ans)
	h.process(ans)
	if h.rep.count(report.KindAttention) != 1 {
		t.Errorf("같은 이유로 보고를 반복하면 안 됨: %v", h.rep.kinds())
	}
	// 25시간 뒤에는 이어서 처리한다.
	h.now = h.now.Add(25 * time.Hour)
	h.process(ans)
	if h.portal.SubmittedCount() != 1 || h.followCall != 1 {
		t.Fatalf("한도가 풀리면 제출해야 함: submitted=%d calls=%d", h.portal.SubmittedCount(), h.followCall)
	}
	if h.caseNow(c.ID).Attention != "" {
		t.Error("제출 후 주의 표시가 지워져야 함")
	}
}

func TestDeferredDraftDoesNotInflateRoundAndGetsSubmittedLater(t *testing.T) {
	// 미뤄 둔 초안이 있어도 다음 점검에서 회차가 올라가 종결되면 안 된다.
	h := newHarness(t, true)
	c, ans := h.answeredCase(1)
	h.portal.LoggedIn = false // 초안은 만들지만 제출은 못 함
	h.process(ans)
	if h.followCall != 1 || len(h.subs(c.ID)) != 2 {
		t.Fatalf("초안은 만들어 둠: calls=%d", h.followCall)
	}
	h.process(ans) // 다시 점검 — 회차 3 으로 계산해 종결하면 버그
	if h.caseNow(c.ID).Status != "open" {
		t.Fatal("미뤄 둔 초안 때문에 사안이 종결되면 안 됨")
	}
	h.portal.LoggedIn = true
	h.process(ans)
	subs := h.subs(c.ID)
	if h.portal.SubmittedCount() != 1 || subs[1].Status != "submitted" {
		t.Fatalf("로그인되면 같은 초안을 제출해야 함: %+v", subs)
	}
	if h.followCall != 1 {
		t.Errorf("초안을 다시 만들지 않고 재사용해야 함: calls=%d", h.followCall)
	}
}

func TestLoginRequiredReportedOnceAndResumes(t *testing.T) {
	h := newHarness(t, true)
	_, ans := h.answeredCase(1)
	h.portal.LoggedIn = false
	h.process(ans)
	h.process(ans)
	if h.rep.count(report.KindAttention) != 1 || !strings.Contains(h.rep.last().Summary, "로그인") {
		t.Fatalf("로그인 필요를 한 번만 보고: %v", h.rep.kinds())
	}
	if !h.answerNow(ans.ID).ProcessedAt.IsZero() {
		t.Fatal("로그인 문제는 미처리로 남겨 이어서 처리해야 함")
	}
	h.portal.LoggedIn = true
	h.process(ans)
	if h.portal.SubmittedCount() != 1 {
		t.Fatal("로그인 후 제출되어야 함")
	}
}

func TestChallengeStopsAutomationForCase(t *testing.T) {
	h := newHarness(t, true)
	c, ans := h.answeredCase(1)
	h.portal.Challenge = true
	h.process(ans)
	got := h.caseNow(c.ID)
	if !got.AutoOff || got.Attention == "" {
		t.Fatalf("추가 인증이 뜨면 이 사안의 자동 처리를 끄고 알려야 함: %+v", got)
	}
	if h.portal.SubmittedCount() != 0 || h.rep.count(report.KindAttention) != 1 {
		t.Errorf("제출 없이 보고 1건: kinds=%v", h.rep.kinds())
	}
	if !strings.Contains(h.rep.last().Body, "나의 민원") || !strings.Contains(h.rep.last().Body, h.followup.Body) {
		t.Error("제출 여부 확인 안내와 초안 전문이 있어야 함")
	}
	pending, _ := h.st.ListPendingAnswers(context.Background())
	if len(pending) != 0 {
		t.Error("자동 처리를 끈 사안의 답변은 다시 처리하지 않아야 함")
	}
}

func TestUnexpectedPageNeverRetries(t *testing.T) {
	h := newHarness(t, true)
	c, ans := h.answeredCase(1)
	h.portal.SubmitErr = portal.ErrUnexpectedPage
	h.process(ans)
	if !h.caseNow(c.ID).AutoOff {
		t.Fatal("제출 여부가 불확실하면 자동으로 다시 시도하지 않고 멈춰야 함")
	}
}

func TestSimilarDraftIsNotAutoSubmitted(t *testing.T) {
	h := newHarness(t, true)
	h.followup.Body = "휴가 반려 기준이 궁금합니다. 기준을 알려 주세요." // 이전 글과 같음
	c, ans := h.answeredCase(1)
	h.process(ans)
	if h.portal.SubmittedCount() != 0 {
		t.Fatal("이전 글과 같은 재민원은 자동 제출하면 안 됨")
	}
	subs := h.subs(c.ID)
	if len(subs) != 2 || subs[1].Status != "draft" {
		t.Fatalf("초안은 남겨 둠: %+v", subs)
	}
	if h.rep.count(report.KindHold) != 1 || !strings.Contains(h.rep.last().Body, "유사도") {
		t.Errorf("사람 확인 보고: %+v", h.rep.last())
	}
	if h.caseNow(c.ID).Status != "open" {
		t.Error("종결하지 않고 열어 둠")
	}
}

func TestEditedOrManualDraftIsNeverAutoSubmitted(t *testing.T) {
	// 1) 자동으로 만든 초안을 사용자가 고친 경우
	h := newHarness(t, true)
	c, ans := h.answeredCase(1)
	h.portal.LoggedIn = false
	h.process(ans)
	subs := h.subs(c.ID)
	h.st.SaveSubmissionText(context.Background(), subs[1].ID, subs[1].Title, "사용자가 고치는 중인 본문", subs[1].AsksJSON)
	h.portal.LoggedIn = true
	h.process(ans)
	if h.portal.SubmittedCount() != 0 || h.rep.count(report.KindHold) != 1 {
		t.Fatalf("사용자가 고친 초안은 자동 제출하면 안 됨: submitted=%d kinds=%v", h.portal.SubmittedCount(), h.rep.kinds())
	}

	// 2) 사용자가 직접 만든 재민원 초안이 이미 있는 경우
	h2 := newHarness(t, true)
	c2, ans2 := h2.answeredCase(1)
	// 화면의 "재민원 초안 만들기"로 만든 초안: AI 가 쓴 그대로(고치지 않음)라 "수정 여부" 검사로는 구분되지 않고,
	// 자동 처리가 만든 초안인지 보는 표식만이 이것을 막는다.
	manual := minwon.DraftResult{Title: "직접 만든 재민원", Body: "직접 만든 본문", Asks: []string{"질문"}}
	js, _ := json.Marshal(manual)
	h2.st.CreateSubmission(context.Background(), c2.ID, "followup", string(js), manual.Title, manual.Body, `["질문"]`)
	h2.process(ans2)
	if h2.portal.SubmittedCount() != 0 || h2.followCall != 0 {
		t.Fatal("직접 만든 초안은 자동 제출하지 않고 AI 도 부르지 않아야 함")
	}
	if h2.rep.count(report.KindHold) != 1 {
		t.Errorf("확인 요청 보고: %v", h2.rep.kinds())
	}
}

// ---------------------------------------------------------------- AI 실패

func TestLLMFailureRetriesThenGivesUp(t *testing.T) {
	h := newHarness(t, true)
	h.evalErr = errors.New("boom")
	c, ans := h.answeredCase(1)

	h.process(ans)
	h.process(ans)
	if h.rep.count(report.KindAttention) != 1 || !h.answerNow(ans.ID).ProcessedAt.IsZero() {
		t.Fatalf("1~2회 실패: 보고 1건, 미처리 유지: %v", h.rep.kinds())
	}
	h.process(ans)
	if h.rep.count(report.KindAttention) != 2 || h.answerNow(ans.ID).ProcessedAt.IsZero() {
		t.Fatalf("3회째에는 포기하고 알림: %v", h.rep.kinds())
	}
	if h.caseNow(c.ID).Status != "open" {
		t.Error("포기해도 종결하지 않음")
	}
	if strings.Contains(h.rep.last().Summary, "boom") {
		t.Error("외부로 나갈 요약에 오류 원문을 넣지 않음")
	}
}

func TestRefusalHoldsImmediately(t *testing.T) {
	h := newHarness(t, true)
	h.evalErr = llm.ErrRefusal
	c, ans := h.answeredCase(1)
	h.process(ans)
	if h.rep.count(report.KindHold) != 1 || h.answerNow(ans.ID).ProcessedAt.IsZero() || h.caseNow(c.ID).Status != "open" {
		t.Fatalf("거절은 재시도하지 않고 바로 보류: %v", h.rep.kinds())
	}
	if h.evalCalls != 1 {
		t.Errorf("재시도하면 안 됨: %d", h.evalCalls)
	}
}

func TestIgnoresClosedAndAutoOffCases(t *testing.T) {
	h := newHarness(t, true)
	c1, a1 := h.answeredCase(1)
	h.st.CloseCase(context.Background(), c1.ID, "x")
	c2, a2 := h.answeredCase(1)
	h.st.SetCaseAutoOff(context.Background(), c2.ID, true)
	h.process(a1)
	h.process(a2)
	if h.evalCalls != 0 || len(h.rep.kinds()) != 0 {
		t.Fatal("종결했거나 자동 처리를 끈 사안은 건드리지 않아야 함")
	}
}

// ---------------------------------------------------------------- 주기 점검

func TestTickImportsPortalAnswerAndProcessesIt(t *testing.T) {
	h := newHarness(t, true)
	h.eval.Recommendation = minwon.RecClose
	h.eval.Items = []minwon.ItemEval{{Verdict: "sufficient", Reason: "ok", Confidence: "high"}}
	ctx := context.Background()
	cid, _ := h.st.CreateCase(ctx, store.Case{Title: "t", Agency: "국방부"})
	sub, _ := h.st.CreateSubmission(ctx, cid, "initial", "", "제목", "본문", `["질의"]`)
	h.st.MarkSubmitted(ctx, sub.ID, "REC-1")

	h.run.Tick(ctx) // 답변이 아직 없음
	if h.evalCalls != 0 {
		t.Fatal("답변이 없으면 아무것도 하지 않음")
	}
	h.portal.SetAnswer("REC-1", "검토 결과를 알려드립니다.", "인사복지실")
	h.now = h.now.Add(7 * time.Hour) // 점검 간격(6시간) 경과
	h.run.Tick(ctx)

	answers, _ := h.st.ListAnswers(ctx, sub.ID)
	if len(answers) != 1 || answers[0].Source != "portal" || answers[0].Dept != "인사복지실" {
		t.Fatalf("사이트 답변이 저장되어야 함: %+v", answers)
	}
	if got, _ := h.st.GetCase(ctx, cid); got.Status != "closed" {
		t.Fatalf("가져온 답변이 자동 처리되어야 함: %+v", got)
	}
}

func TestPortalProblemReportedOncePerCause(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	cid, _ := h.st.CreateCase(ctx, store.Case{Title: "t"})
	sub, _ := h.st.CreateSubmission(ctx, cid, "initial", "", "제목", "본문", `["질의"]`)
	h.st.MarkSubmitted(ctx, sub.ID, "REC-1")

	h.portal.LoggedIn = false
	for i := 0; i < 3; i++ {
		h.now = h.now.Add(7 * time.Hour)
		h.run.Tick(ctx)
	}
	if h.rep.count(report.KindAttention) != 1 {
		t.Fatalf("같은 문제는 한 번만 보고: %v", h.rep.kinds())
	}
	// 복구되었다가 다시 문제가 되면 새로 보고한다.
	h.portal.LoggedIn = true
	h.now = h.now.Add(7 * time.Hour)
	h.run.Tick(ctx)
	h.portal.LoggedIn = false
	h.now = h.now.Add(7 * time.Hour)
	h.run.Tick(ctx)
	if h.rep.count(report.KindAttention) != 2 {
		t.Fatalf("복구 뒤 재발은 다시 보고: %v", h.rep.kinds())
	}
}

func TestTickRespectsSettings(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, true)
	h.cfg.Auto.Enabled = false
	_, ans := h.answeredCase(1)
	h.run.Tick(ctx)
	h.run.ProcessPending(ctx)
	h.run.CheckNow(ctx)
	if h.evalCalls != 0 || !h.answerNow(ans.ID).ProcessedAt.IsZero() {
		t.Fatal("꺼져 있으면 아무것도 하지 않아야 함")
	}

	h2 := newHarness(t, true)
	h2.cfg.Auto.AnswerAuto = false
	_, ans2 := h2.answeredCase(1)
	h2.run.Tick(ctx)
	if h2.evalCalls != 0 || !h2.answerNow(ans2.ID).ProcessedAt.IsZero() {
		t.Fatal("답변 자동 처리가 꺼져 있으면 평가하지 않아야 함")
	}
}

func TestDailySummary(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, false)
	h.now = time.Date(2026, 10, 23, 21, 30, 0, 0, time.Local)
	h.cfg.Auto.DailySummaryHour = 21

	h.run.Tick(ctx)
	if h.rep.count(report.KindDaily) != 0 {
		t.Fatal("활동이 없으면 요약을 보내지 않음")
	}
	// 활동 만들기: 종결 1건, 확인 필요 1건
	h.st.SetClock(func() time.Time { return h.now.Add(-3 * time.Hour) })
	h.st.AddReport(ctx, store.Report{CaseID: 1, Kind: report.KindClosed, Title: "사안을 종결했습니다"})
	h.st.AddReport(ctx, store.Report{CaseID: 2, Kind: report.KindHold, Title: "판단을 보류했습니다"})
	h.st.SetClock(func() time.Time { return h.now })
	h.cfg.Auto.DailySummaryHour = 21
	// 위 Tick 에서 이미 오늘 날짜가 기록되었으므로 다음 날로 넘겨서 확인한다.
	h.now = h.now.Add(24 * time.Hour)
	h.st.SetClock(func() time.Time { return h.now.Add(-3 * time.Hour) })
	h.st.AddReport(ctx, store.Report{CaseID: 1, Kind: report.KindClosed, Title: "사안을 종결했습니다"})
	h.st.AddReport(ctx, store.Report{CaseID: 2, Kind: report.KindHold, Title: "판단을 보류했습니다"})
	h.st.SetClock(func() time.Time { return h.now })

	h.now = time.Date(2026, 10, 24, 20, 0, 0, 0, time.Local) // 설정 시각 이전
	h.run.Tick(ctx)
	if h.rep.count(report.KindDaily) != 0 {
		t.Fatal("설정 시각 전에는 보내지 않음")
	}
	h.now = time.Date(2026, 10, 24, 21, 10, 0, 0, time.Local)
	h.run.Tick(ctx)
	if h.rep.count(report.KindDaily) != 1 {
		t.Fatalf("설정 시각 이후 한 번 보냄: %v", h.rep.kinds())
	}
	d := h.rep.last()
	if !strings.Contains(d.Summary, "종결 1건") || !strings.Contains(d.Summary, "확인 필요 1건") || !strings.Contains(d.Body, "사안 #2") {
		t.Fatalf("요약 내용: %+v", d)
	}
	h.run.Tick(ctx)
	if h.rep.count(report.KindDaily) != 1 {
		t.Error("하루에 한 번만")
	}
	h.cfg.Auto.DailySummaryHour = -1
	h.now = h.now.Add(24 * time.Hour)
	h.run.Tick(ctx)
	if h.rep.count(report.KindDaily) != 1 {
		t.Error("-1 이면 보내지 않음")
	}
}
