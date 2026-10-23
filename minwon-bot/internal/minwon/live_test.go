//go:build live

// 실제 AI 를 부르는 수동 점검용 테스트다. 구독/API 사용량이 들어가므로 기본 빌드에서는 빠진다.
//
//	go test -tags live ./internal/minwon -run Live -v
//
// MINWON_LIVE_BACKEND=api 로 API 키 방식을, MINWON_LIVE_MODEL 로 모델을 바꾼다.
package minwon

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"minwon-bot/internal/llm"
)

func liveLLM(t *testing.T) llm.LLM {
	t.Helper()
	model := os.Getenv("MINWON_LIVE_MODEL")
	if os.Getenv("MINWON_LIVE_BACKEND") == "api" {
		if model == "" {
			model = "claude-opus-5-5"
		}
		return llm.NewAPI(model, "high", 5*time.Minute)
	}
	if model == "" {
		model = "opus"
	}
	return llm.NewClaudeCode("", model, 5*time.Minute)
}

func show(t *testing.T, label string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	t.Logf("%s:\n%s", label, b)
}

func TestLiveDraftEvaluateFollowUp(t *testing.T) {
	ctx := context.Background()
	svc := New(liveLLM(t), []string{"이름:홍길동"})

	// 가상의 사안이다.
	in := DraftInput{
		Agency: "국방부",
		Facts: "홍길동 상병입니다. 2026년 9월 첫째 주에 정기휴가를 신청했는데 중대장이 훈련 일정을 이유로 반려했습니다. " +
			"같은 기간 다른 소대원들의 휴가는 승인되었습니다. 연락처는 010-1234-5678 입니다.",
		Desired: "휴가 반려 기준이 무엇인지 알고 싶고, 형평에 맞게 처리되었는지 확인하고 싶습니다.",
	}
	d, err := svc.Draft(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	show(t, "초안", d)
	if d.NeedsInfo {
		t.Logf("되묻기가 나왔습니다. 추가 답을 붙여 다시 작성합니다.")
		for _, q := range d.Questions {
			in.Extra = append(in.Extra, QA{Question: q, Answer: "정확한 날짜는 9월 2일부터 6일까지였고, 소속은 제7사단입니다."})
		}
		if d, err = svc.Draft(ctx, in); err != nil {
			t.Fatal(err)
		}
		show(t, "추가 답 이후 초안", d)
	}

	answer := "귀하의 민원에 대한 검토 결과를 알려드립니다. 휴가는 군인복무기본법 및 관련 훈령에 따라 지휘관이 부대 사정을 고려하여 승인 여부를 결정하고 있습니다. " +
		"귀하의 휴가 건 역시 관련 규정에 따라 적정하게 처리된 것으로 확인됩니다. 앞으로도 장병 복지 향상을 위해 노력하겠습니다."
	e, err := svc.Evaluate(ctx, EvalInput{Title: d.Title, Body: d.Body, Asks: d.Asks, Answer: answer, Dept: "인사복지실", Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	show(t, "평가", e)

	f, err := svc.FollowUp(ctx, FollowUpInput{
		Agency: "국방부", Round: 2, PrevReceipt: "2AA-2610-0001", PrevSubmitAt: "2026년 10월 1일",
		PrevTitle: d.Title, PrevBody: d.Body, PrevAsks: d.Asks, Answer: answer, Dept: "인사복지실", Eval: e,
	})
	if err != nil {
		t.Fatal(err)
	}
	show(t, "재민원", f)
}

func TestLiveStyle(t *testing.T) {
	svc := New(liveLLM(t), nil)
	p, err := svc.ExtractStyle(context.Background(), []StyleText{
		{Text: "안녕하세요. 휴가 승인 기준이 궁금해서 글 남깁니다. 제가 확인한 건 두 가지입니다.\n1) 반려 사유\n2) 이의신청 방법\n짧게라도 답 부탁드립니다. 감사합니다."},
		{Text: "급식 검수 기록이 공개 대상인지 문의합니다.\n1) 공개 대상 여부\n2) 청구 방법\n확인 부탁드립니다."},
	})
	if err != nil {
		t.Fatal(err)
	}
	show(t, "문체 프로필", p)
}
