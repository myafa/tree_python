package minwon

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"minwon-bot/internal/config"
	"minwon-bot/internal/privacy"
)

type Severity string

const (
	// SevBlock 은 자동 처리를 멈추고 사람이 확인해야 한다는 뜻이다.
	SevBlock Severity = "block"
	SevWarn  Severity = "warn"
)

// Finding 은 제출 전 점검에서 발견한 문제 한 건이다.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

type GuardInput struct {
	Round            int
	Title, Body      string
	PrevBodies       []string // 같은 사안의 이전 회차 본문
	SubmittedLast24h int      // 모든 사안을 합친 최근 24시간 제출 건수
	LastSubmittedAt  time.Time
	Now              time.Time
	Limits           config.Limits
}

// 욕설과 직접적인 협박 표현. "죽음", "자살" 같은 말은 사망사고 민원에서 정당하게 쓰이므로 넣지 않는다.
var profanity = []string{
	"씨발", "시발", "씹", "ㅅㅂ", "ㅂㅅ", "개새끼", "병신", "좆", "지랄", "미친놈", "미친년", "썅", "염병", "또라이",
	"엿먹", "닥쳐", "꺼져",
	"죽여버", "죽이겠", "죽일 거", "죽여 버", "가만 안 두", "가만두지 않", "가만 두지 않", "찾아가겠",
}

// Check 는 제출 전에 사람이 봐야 할 문제를 모은다. 자동 처리 여부는 HasBlock 으로 판단한다.
func Check(in GuardInput) []Finding {
	var out []Finding
	add := func(code string, sev Severity, format string, a ...any) {
		out = append(out, Finding{Code: code, Severity: sev, Message: fmt.Sprintf(format, a...)})
	}
	l := in.Limits

	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Body) == "" {
		add("empty", SevBlock, "제목이나 본문이 비어 있습니다.")
	}
	if in.Round >= l.RepeatRound {
		add("repeat_round", SevBlock,
			"같은 사안의 %d회차 민원입니다. 3회 이상 반복된 민원은 내용이 같지 않아도 종결 처리될 수 있다는 보도가 있습니다(2024년 민원처리법 개정, 조문 확인 필요). 고충민원, 정보공개청구 같은 다른 경로를 먼저 검토하세요.",
			in.Round)
	}
	maxSim := 0.0
	for _, p := range in.PrevBodies {
		if s := Similarity(p, in.Body); s > maxSim {
			maxSim = s
		}
	}
	switch {
	case maxSim >= 0.9:
		add("similar_to_previous", SevBlock, "이전 회차와 거의 같은 내용입니다(유사도 %d%%). 같은 민원을 다시 내면 중복·반복 민원으로 종결될 수 있습니다.", pct(maxSim))
	case maxSim >= l.Similarity:
		add("similar_to_previous", SevWarn, "이전 회차와 내용이 비슷합니다(유사도 %d%%). 아직 답을 못 받은 점만 구체적으로 묻는 글인지 확인하세요.", pct(maxSim))
	}
	if in.SubmittedLast24h >= l.DailyMax {
		add("daily_limit", SevBlock, "최근 24시간에 이미 %d건을 제출했습니다(하루 한도 %d건).", in.SubmittedLast24h, l.DailyMax)
	}
	if !in.LastSubmittedAt.IsZero() {
		gap := time.Duration(l.MinGapMinutes) * time.Minute
		if wait := gap - in.Now.Sub(in.LastSubmittedAt); wait > 0 {
			add("min_gap", SevBlock, "직전 제출 후 최소 간격(%d분)이 지나지 않았습니다. %d분 뒤에 제출하세요.",
				l.MinGapMinutes, int(wait.Minutes())+1)
		}
	}
	if words := FindProfanity(in.Title + "\n" + in.Body); len(words) > 0 {
		add("profanity", SevBlock, "욕설·모욕·협박으로 보일 수 있는 표현이 있습니다(%s). 이런 표현이 상당 부분 포함되면 종결 처리될 수 있으니 고쳐 쓰세요.",
			strings.Join(words, ", "))
	}
	if pii := privacy.Scan(in.Body); len(pii) > 0 {
		var parts []string
		for _, f := range pii {
			parts = append(parts, fmt.Sprintf("%s %d건", f.Label, f.Count))
		}
		add("pii", SevWarn, "본문에 개인정보로 보이는 값이 있습니다(%s). 신청인 정보 입력란에 이미 있다면 본문에서 빼고, 공개 여부는 비공개로 하세요.",
			strings.Join(parts, ", "))
	}
	return out
}

func HasBlock(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == SevBlock {
			return true
		}
	}
	return false
}

func pct(f float64) int { return int(f*100 + 0.5) }

// FindProfanity 는 text 에서 발견된 욕설·협박 표현 목록(중복 제거)이다.
func FindProfanity(text string) []string {
	t := strings.ToLower(text)
	var found []string
	for _, w := range profanity {
		if strings.Contains(t, w) {
			found = append(found, w)
		}
	}
	return found
}

// Similarity 는 두 글의 글자 2-gram Dice 계수(0~1)다. 띄어쓰기와 문장부호는 무시한다.
func Similarity(a, b string) float64 {
	ga, gb := bigrams(a), bigrams(b)
	var na, nb int
	for _, c := range ga {
		na += c
	}
	for _, c := range gb {
		nb += c
	}
	if na == 0 || nb == 0 {
		return 0
	}
	inter := 0
	for g, ca := range ga {
		if cb, ok := gb[g]; ok {
			inter += min(ca, cb)
		}
	}
	return 2 * float64(inter) / float64(na+nb)
}

func bigrams(s string) map[string]int {
	var rs []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			rs = append(rs, r)
		}
	}
	m := map[string]int{}
	for i := 0; i+1 < len(rs); i++ {
		m[string(rs[i:i+2])]++
	}
	return m
}
