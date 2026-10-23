package minwon

import (
	"strings"
	"testing"
	"time"

	"minwon-bot/internal/config"
)

func baseInput() GuardInput {
	return GuardInput{
		Round: 1, Title: "휴가 승인 기준 문의", Body: "복무 중 휴가 승인 기준이 부대마다 다릅니다. 기준을 알려 주세요.",
		Now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC), Limits: config.Default().Limits,
	}
}

func codes(fs []Finding) string {
	var c []string
	for _, f := range fs {
		c = append(c, f.Code+":"+string(f.Severity))
	}
	return strings.Join(c, ",")
}

func TestCleanDraftHasNoFindings(t *testing.T) {
	if fs := Check(baseInput()); len(fs) != 0 {
		t.Fatalf("문제가 없어야 함: %s", codes(fs))
	}
}

func TestRepeatRoundBlocks(t *testing.T) {
	in := baseInput()
	in.Round = 2
	if HasBlock(Check(in)) {
		t.Fatal("2회차는 막지 않음")
	}
	in.Round = 3
	if !strings.Contains(codes(Check(in)), "repeat_round:block") {
		t.Fatalf("3회차는 막아야 함: %s", codes(Check(in)))
	}
}

func TestSimilarityWarnAndBlock(t *testing.T) {
	prev := "복무 중 휴가 승인 기준이 부대마다 다릅니다. 기준을 알려 주세요."
	in := baseInput()

	in.PrevBodies = []string{prev}
	if !strings.Contains(codes(Check(in)), "similar_to_previous:block") {
		t.Fatalf("같은 글은 막아야 함: %s", codes(Check(in)))
	}
	in.PrevBodies = []string{"전혀 다른 주제로 급식 질이 나빠서 개선을 요청합니다. 식단표와 검수 기록을 공개해 주십시오."}
	if len(Check(in)) != 0 {
		t.Fatalf("다른 글은 통과해야 함: %s", codes(Check(in)))
	}
	s := Similarity(prev, strings.Replace(prev, "알려 주세요", "알려주십시오", 1))
	if s < 0.6 || s >= 1 {
		t.Fatalf("조금 바꾼 글의 유사도는 0.6~1 사이여야 함: %.2f", s)
	}
	if Similarity("", "abc") != 0 {
		t.Fatal("빈 글은 0")
	}
}

func TestDailyLimitAndMinGap(t *testing.T) {
	in := baseInput()
	in.SubmittedLast24h = 3
	if !strings.Contains(codes(Check(in)), "daily_limit:block") {
		t.Fatalf("하루 한도 초과는 막아야 함: %s", codes(Check(in)))
	}
	in.SubmittedLast24h = 2
	in.LastSubmittedAt = in.Now.Add(-4 * time.Minute)
	got := Check(in)
	if !strings.Contains(codes(got), "min_gap:block") || !strings.Contains(got[0].Message, "7분") {
		t.Fatalf("최소 간격 위반은 남은 시간을 알려야 함: %v", got)
	}
	in.LastSubmittedAt = in.Now.Add(-11 * time.Minute)
	if len(Check(in)) != 0 {
		t.Fatalf("간격이 지나면 통과: %s", codes(Check(in)))
	}
}

func TestProfanityAndThreat(t *testing.T) {
	in := baseInput()
	in.Body = "이 병신 같은 처리는 뭐냐. 가만 안 두겠다."
	if !strings.Contains(codes(Check(in)), "profanity:block") {
		t.Fatalf("욕설·협박은 막아야 함: %s", codes(Check(in)))
	}
	in.Body = "훈련 중 사망사고가 발생했고 자살 여부에 대한 조사 결과를 알려 주세요."
	if len(Check(in)) != 0 {
		t.Fatalf("사망·자살 언급은 정당한 민원이므로 통과해야 함: %s", codes(Check(in)))
	}
}

func TestPIIWarningHasNoValue(t *testing.T) {
	in := baseInput()
	in.Body = "제 번호는 010-1234-5678 입니다. 답변 바랍니다."
	fs := Check(in)
	if !strings.Contains(codes(fs), "pii:warn") || HasBlock(fs) {
		t.Fatalf("개인정보는 경고만: %s", codes(fs))
	}
	if strings.Contains(fs[0].Message, "5678") {
		t.Fatal("경고 문구에 값을 넣으면 안 됨")
	}
}

func TestEmptyBlocks(t *testing.T) {
	in := baseInput()
	in.Body = "  "
	if !strings.Contains(codes(Check(in)), "empty:block") {
		t.Fatal("빈 본문은 막아야 함")
	}
}
