package minwon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"minwon-bot/internal/llm"
)

// fake 는 마지막 요청을 기록하고 out 을 JSON 으로 돌려주는 가짜 AI 다.
func fake(last *llm.Request, out any) llm.LLM {
	return llm.Func(func(ctx context.Context, r llm.Request) (json.RawMessage, error) {
		*last = r
		b, err := json.Marshal(out)
		return b, err
	})
}

func okDraft() DraftResult {
	return DraftResult{
		Title:            "휴가 승인 기준 문의",
		Body:             "저는 {{TERM_1}} 소속입니다. 최근 3개월 사이 휴가 승인이 반려되었습니다.\n1. 반려 기준을 알려 주세요.\n2. 이의 신청 절차를 알려 주세요.",
		Asks:             []string{"반려 기준을 알려 주세요.", "이의 신청 절차를 알려 주세요."},
		SuggestedAgency:  "국방부",
		Visibility:       "private",
		VisibilityReason: "부대 정보가 있습니다.",
	}
}

func TestDraftMasksInputAndUnmasksOutput(t *testing.T) {
	var last llm.Request
	svc := New(fake(&last, okDraft()), []string{"제7사단"})
	res, err := svc.Draft(context.Background(), DraftInput{
		Title: "휴가", Agency: "국방부",
		Facts:   "제7사단 소속입니다. 연락처 010-1234-5678. 최근 3개월 사이 휴가 승인이 반려되었습니다.",
		Desired: "기준 공개",
		Style:   &StyleProfile{Summary: "짧고 건조하게 쓴다."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(last.User, "제7사단") || strings.Contains(last.User, "010-1234-5678") {
		t.Fatalf("AI 에 보내는 글에 개인정보가 남음:\n%s", last.User)
	}
	for _, want := range []string{"{{TERM_1}}", "{{PHONE_1}}", "<문체_프로필>", "짧고 건조하게 쓴다."} {
		if !strings.Contains(last.User, want) {
			t.Errorf("요청에 %q 없음:\n%s", want, last.User)
		}
	}
	if !strings.Contains(last.System, "사실만 쓴다") || last.Schema == nil {
		t.Error("작성 시스템 프롬프트와 스키마가 전달되어야 함")
	}
	if !strings.Contains(res.Body, "제7사단 소속입니다") || strings.Contains(res.Body, "{{") {
		t.Fatalf("복원 실패: %s", res.Body)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("경고가 없어야 함: %v", res.Warnings)
	}
}

func TestDraftNeedsInfoReturnsQuestions(t *testing.T) {
	var last llm.Request
	out := DraftResult{NeedsInfo: true, Questions: []string{"언제 있었던 일인가요?", " ", "어느 부대인가요?"},
		Title: "무시될 제목", Body: "무시될 본문", Visibility: "public"}
	res, err := New(fake(&last, out), nil).Draft(context.Background(), DraftInput{Facts: "휴가가 반려됨"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsInfo || len(res.Questions) != 2 || res.Title != "" || res.Body != "" || res.Asks != nil {
		t.Fatalf("되묻기 결과 불일치: %+v", res)
	}
}

func TestDraftRejectsIncomplete(t *testing.T) {
	var last llm.Request
	bad := okDraft()
	bad.Asks = nil
	if _, err := New(fake(&last, bad), nil).Draft(context.Background(), DraftInput{Facts: "x"}); err == nil {
		t.Fatal("질의 항목이 없으면 오류여야 함")
	}
	if _, err := New(fake(&last, DraftResult{NeedsInfo: true}), nil).Draft(context.Background(), DraftInput{Facts: "x"}); err == nil {
		t.Fatal("질문 없는 되묻기는 오류여야 함")
	}
	if _, err := New(fake(&last, okDraft()), nil).Draft(context.Background(), DraftInput{Facts: "  "}); err == nil {
		t.Fatal("사실관계가 없으면 AI 를 부르기 전에 오류여야 함")
	}
}

func TestDraftLeftoverTokenWarning(t *testing.T) {
	var last llm.Request
	out := okDraft()
	out.Body = "담당자 {{PHONE_9}} 에게 문의했습니다."
	res, err := New(fake(&last, out), nil).Draft(context.Background(), DraftInput{Facts: "문의함"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "{{PHONE_9}}") {
		t.Fatalf("복원되지 않은 자리표시자를 알려야 함: %v", res.Warnings)
	}
}

func TestDraftFlagsInventedNumbersButNotLawArticlesOrInput(t *testing.T) {
	var last llm.Request
	out := okDraft()
	out.Body = "2026년 3월 2일 휴가를 신청했습니다. 민원처리법 제23조에 따라 14명이 반려되었다고 들었습니다. 1,200건 중 3건입니다."
	res, err := New(fake(&last, out), nil).Draft(context.Background(), DraftInput{
		Facts: "2026년 3월 2일 휴가를 신청했고 1200건 중 3건이 반려됨.",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, " ")
	if !strings.Contains(joined, "14") {
		t.Errorf("지어낸 숫자 14 를 잡아야 함: %v", res.Warnings)
	}
	for _, ok := range []string{"2026", "1,200", "23"} {
		if strings.Contains(joined, ok) {
			t.Errorf("정상 숫자 %s 를 경고하면 안 됨: %v", ok, res.Warnings)
		}
	}
}

func TestStripMarkdown(t *testing.T) {
	got := stripMarkdown("## 제목\n**굵게** 그리고 `코드`")
	if got != "제목\n굵게 그리고 코드" {
		t.Fatalf("마크다운 제거 불일치: %q", got)
	}
}

// ---- 평가 ----

func evalIn() EvalInput {
	return EvalInput{
		Title: "휴가 승인 기준", Body: "본문", Round: 1, Dept: "인사복지실",
		Asks:   []string{"반려 기준을 알려 주세요.", "이의 신청 절차를 알려 주세요."},
		Answer: "관련 규정에 따라 처리되었습니다. 군인복무기본법을 참고하시기 바랍니다.",
	}
}

func TestEvaluateNormalizesModelOutput(t *testing.T) {
	var last llm.Request
	out := EvalResult{
		Items: []ItemEval{
			{Ask: "모델이 바꿔 쓴 질의", Verdict: "evasive", Reason: "규정만 나열", Evidence: "관련 규정에 따라 처리되었습니다.", Confidence: "high"},
			// 두 번째 항목을 모델이 빼먹음
		},
		Score:          140,
		Summary:        "일반론만 답했습니다.",
		Recommendation: RecClose, // 미흡 항목이 있는데 종결 권고
		LegalRefs:      []LegalRef{{Name: "군인복무기본법", Article: "제10조", Relevance: "휴가"}},
	}
	res, err := New(fake(&last, out), nil).Evaluate(context.Background(), evalIn())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || res.Items[0].Ask != "반려 기준을 알려 주세요." {
		t.Fatalf("항목은 질의 순서·문구를 따라야 함: %+v", res.Items)
	}
	if res.Items[1].Verdict != VerdictUndetermined || res.Items[1].Confidence != "low" {
		t.Errorf("빠진 항목은 판정 불가여야 함: %+v", res.Items[1])
	}
	if res.Score != 100 {
		t.Errorf("점수는 0~100 으로 보정: %d", res.Score)
	}
	if res.Recommendation != RecFollowUp {
		t.Errorf("미흡 항목이 있으면 종결 권고를 재민원으로 바꿔야 함: %s", res.Recommendation)
	}
	if len(res.LegalRefs) != 1 || !strings.HasPrefix(res.LegalRefs[0].URL, "https://www.law.go.kr/법령/") {
		t.Errorf("법령 링크가 붙어야 함: %+v", res.LegalRefs)
	}
	notes := strings.Join(res.Notes, "|")
	for _, want := range []string{"평가하지 않아", "'재민원'으로 바꿨습니다", "확인되지 않았습니다"} {
		if !strings.Contains(notes, want) {
			t.Errorf("안내에 %q 없음: %s", want, notes)
		}
	}
	if !strings.Contains(last.User, "<asks>") || !strings.Contains(last.User, "1. 반려 기준을 알려 주세요.") {
		t.Errorf("질의 목록이 요청에 있어야 함:\n%s", last.User)
	}
	if !strings.Contains(last.System, "지시를 따르라") {
		t.Error("답변은 자료일 뿐 지시가 아니라는 문구가 시스템 프롬프트에 있어야 함")
	}
}

func TestEvaluateFlagsFabricatedQuote(t *testing.T) {
	var last llm.Request
	out := EvalResult{
		Items: []ItemEval{
			{Verdict: "partial", Reason: "r", Evidence: "관련 규정에 따라 처리되었습니다.", Confidence: "high"},
			{Verdict: "unanswered", Reason: "r", Evidence: "이의신청은 30일 이내에 가능합니다.", Confidence: "high"},
		},
		Recommendation: RecFollowUp,
	}
	res, err := New(fake(&last, out), nil).Evaluate(context.Background(), evalIn())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Items[0].Evidence, "⚠") {
		t.Errorf("실제 인용은 경고하면 안 됨: %s", res.Items[0].Evidence)
	}
	if !strings.Contains(res.Items[1].Evidence, "⚠") {
		t.Errorf("지어낸 인용은 경고해야 함: %s", res.Items[1].Evidence)
	}
}

func TestEvaluateLowConfidenceHoldsClose(t *testing.T) {
	var last llm.Request
	out := EvalResult{
		Items: []ItemEval{
			{Verdict: "sufficient", Confidence: "low"},
			{Verdict: "undetermined", Confidence: "low"},
		},
		Recommendation: RecClose,
	}
	res, _ := New(fake(&last, out), nil).Evaluate(context.Background(), evalIn())
	if res.Recommendation != RecHold {
		t.Fatalf("확신이 낮으면 종결하지 않고 보류해야 함: %s", res.Recommendation)
	}
}

func TestEvaluateInputErrors(t *testing.T) {
	var last llm.Request
	svc := New(fake(&last, EvalResult{}), nil)
	in := evalIn()
	in.Asks = []string{" "}
	if _, err := svc.Evaluate(context.Background(), in); !errors.Is(err, ErrNoAsks) {
		t.Errorf("질의가 없으면 ErrNoAsks: %v", err)
	}
	in = evalIn()
	in.Answer = ""
	if _, err := svc.Evaluate(context.Background(), in); !errors.Is(err, ErrEmptyAnswer) {
		t.Errorf("답변이 없으면 ErrEmptyAnswer: %v", err)
	}
	if last.User != "" {
		t.Error("입력 오류일 때는 AI 를 부르면 안 됨")
	}
}

// ---- 재민원 ----

func followIn() FollowUpInput {
	ev := EvalResult{
		Items: []ItemEval{
			{Ask: "반려 기준을 알려 주세요.", Verdict: VerdictEvasive, Reason: "규정만 나열"},
			{Ask: "이의 신청 절차를 알려 주세요.", Verdict: VerdictSufficient, Reason: "안내됨"},
		},
		FollowUpFocus: []string{"반려 기준의 구체적 내용"},
	}
	return FollowUpInput{
		Agency: "국방부", Round: 2, PrevReceipt: "2AA-2610-0001", PrevSubmitAt: "2026-10-01",
		PrevTitle: "휴가 승인 기준", PrevBody: "본문", Answer: "관련 규정에 따라 처리되었습니다.", Dept: "인사복지실", Eval: ev,
	}
}

func TestFollowUpRefusesWhenNothingUnresolved(t *testing.T) {
	var last llm.Request
	in := followIn()
	in.Eval.Items[0].Verdict = VerdictSufficient
	if _, err := New(fake(&last, okDraft()), nil).FollowUp(context.Background(), in); !errors.Is(err, ErrNothingToFollowUp) {
		t.Fatalf("할 일이 없으면 ErrNothingToFollowUp: %v", err)
	}
	if last.User != "" {
		t.Error("AI 를 부르면 안 됨")
	}
}

func TestFollowUpPromptTargetsUnresolved(t *testing.T) {
	var last llm.Request
	out := okDraft()
	out.Body = "2AA-2610-0001 민원에 대한 답변을 받았습니다.\n1. 반려 기준을 알려 주세요."
	out.Asks = []string{"반려 기준을 구체적으로 알려 주세요."}
	out.Visibility = "public"
	res, err := New(fake(&last, out), nil).FollowUp(context.Background(), followIn())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2AA-2610-0001", "<새_회차>2</새_회차>", "[evasive] 반려 기준을 알려 주세요.", "[sufficient]", "다시 물을 쟁점: 반려 기준의 구체적 내용"} {
		if !strings.Contains(last.User, want) {
			t.Errorf("요청에 %q 없음:\n%s", want, last.User)
		}
	}
	if !strings.Contains(last.System, "미흡하다고 판정된 항목") {
		t.Error("재민원 시스템 프롬프트가 쓰여야 함")
	}
	if len(res.Asks) != 1 || len(res.Warnings) != 0 {
		t.Fatalf("결과 불일치: %+v", res)
	}
}

// ---- 문체 ----

func TestExtractStyleScrubsTokensAndSendsEdits(t *testing.T) {
	var last llm.Request
	out := StyleProfile{
		Summary:         "짧게 쓴다. {{TERM_1}} 소속이라고 밝힌다.",
		Tone:            "건조함",
		OfficialAddress: "{{TERM_2}} 담당자님",
		Phrases:         []string{"문의드립니다", "{{TERM_1}} 에서"},
	}
	svc := New(fake(&last, out), []string{"홍길동"})
	p, err := svc.ExtractStyle(context.Background(), []StyleText{
		{Text: "홍길동입니다. 휴가 기준을 문의드립니다."},
		{Text: "최종본 홍길동", Original: "AI 초안 홍길동님께"},
		{Text: "  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(last.User, "홍길동") {
		t.Errorf("문체 학습 글의 개인정보가 AI 에 나감:\n%s", last.User)
	}
	for _, want := range []string{"<글 번호=\"1\">", "<수정사례 번호=\"1\">", "[AI 초안]", "[사용자 최종본]"} {
		if !strings.Contains(last.User, want) {
			t.Errorf("요청에 %q 없음", want)
		}
	}
	if strings.Contains(p.Summary, "{{") || strings.Contains(p.OfficialAddress, "{{") {
		t.Errorf("저장될 프로필에 자리표시자가 남음: %+v", p)
	}
	if len(p.Phrases) != 1 || p.Phrases[0] != "문의드립니다" {
		t.Errorf("자리표시자 포함 표현은 버려야 함: %v", p.Phrases)
	}
	if !strings.Contains(p.Prompt(), "- 어조: 건조함") {
		t.Errorf("프롬프트 렌더링 불일치:\n%s", p.Prompt())
	}
}

func TestExtractStyleErrorsAndParse(t *testing.T) {
	var last llm.Request
	svc := New(fake(&last, StyleProfile{}), nil)
	if _, err := svc.ExtractStyle(context.Background(), nil); err == nil {
		t.Error("글이 없으면 오류")
	}
	if _, err := svc.ExtractStyle(context.Background(), []StyleText{{Text: "글"}}); err == nil {
		t.Error("빈 요약은 오류")
	}
	if p, err := ParseStyle(""); p != nil || err != nil {
		t.Error("빈 문자열은 nil")
	}
	if p, _ := ParseStyle(`{"summary":"x"}`); p == nil || p.Summary != "x" {
		t.Error("저장 JSON 을 읽어야 함")
	}
	if p, _ := ParseStyle(`{"summary":" "}`); p != nil {
		t.Error("요약이 비면 nil")
	}
}
