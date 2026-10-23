// Package privacy 는 AI 에 보내기 전에 개인정보를 자리표시자로 바꾸고,
// 받은 결과에서 로컬로 되돌린다. 원문은 이 PC 밖으로 나가지 않는다.
package privacy

import (
	"regexp"
	"sort"
	"strings"
)

// 검사 순서가 중요하다. 더 구체적인 패턴(주민번호, 군번)을 먼저 적용해야
// 전화번호나 계좌번호 패턴이 그 일부를 먼저 가져가지 않는다.
var detectors = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"RRN", regexp.MustCompile(`\b\d{6}\s?-\s?[1-8]\d{6}\b`)},
	{"MILID", regexp.MustCompile(`\b\d{2}-\d{7,8}\b`)},
	{"PHONE", regexp.MustCompile(`\b(?:01[016789]|02|0[3-6][1-5]|070|080)[-.\s]?\d{3,4}[-.\s]?\d{4}\b`)},
	{"EMAIL", regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)},
	{"ACCOUNT", regexp.MustCompile(`\b\d{2,6}-\d{2,6}-\d{2,8}(?:-\d{1,4})?\b`)},
}

var kindLabel = map[string]string{
	"RRN":     "주민등록번호",
	"MILID":   "군번 또는 유사한 번호",
	"PHONE":   "전화번호",
	"EMAIL":   "이메일",
	"ACCOUNT": "계좌번호 또는 유사한 번호",
	"TERM":    "지정한 낱말",
	"NAME":    "사람 이름",
	"UNIT":    "부대·조직 이름",
	"ORG":     "기관 이름",
}

// AI 에게 알려 줄 자리표시자 설명. 값은 알리지 않고 종류만 알린다.
var kindHint = map[string]string{
	"RRN":     "주민등록번호",
	"MILID":   "군번 등 번호",
	"PHONE":   "전화번호",
	"EMAIL":   "이메일 주소",
	"ACCOUNT": "계좌 등 번호",
	"TERM":    "가려 둔 낱말(종류 알 수 없음, 입력에서 놓인 자리 그대로 쓸 것)",
	"NAME":    "사람 이름",
	"UNIT":    "부대·조직 이름",
	"ORG":     "기관 이름",
}

// 사용자가 낱말 앞에 붙일 수 있는 종류 표시. 예: "이름:홍길동", "부대:제7사단".
var termKinds = map[string]string{"이름": "NAME", "부대": "UNIT", "기관": "ORG"}

type term struct{ kind, value string }

func parseTerm(s string) term {
	s = strings.TrimSpace(s)
	for _, sep := range []string{":", "："} {
		if label, rest, ok := strings.Cut(s, sep); ok {
			if kind, known := termKinds[strings.TrimSpace(label)]; known {
				if v := strings.TrimSpace(rest); v != "" {
					return term{kind, v}
				}
			}
		}
	}
	return term{"TERM", s}
}

// Masker 는 한 번의 요청 안에서 쓰는 가림/복원 도구다. 동시에 쓰지 않는다.
type Masker struct {
	terms []term
	fwd   map[string]string // 원문 -> 자리표시자
	rev   map[string]string // 자리표시자 -> 원문
	count map[string]int
}

// NewMasker 의 terms 는 이름, 부대명처럼 사용자가 직접 지정한 낱말이다.
// "이름:홍길동", "부대:제7사단", "기관:국방부" 처럼 종류를 앞에 붙이면
// AI 가 자리표시자의 종류를 알 수 있어 문장이 어색해지는 일이 줄어든다.
func NewMasker(terms []string) *Masker {
	var clean []term
	for _, raw := range terms {
		if t := parseTerm(raw); t.value != "" {
			clean = append(clean, t)
		}
	}
	// 긴 낱말을 먼저 바꿔야 "홍길동"이 "홍길"보다 먼저 처리된다.
	sort.SliceStable(clean, func(i, j int) bool { return len([]rune(clean[i].value)) > len([]rune(clean[j].value)) })
	return &Masker{terms: clean, fwd: map[string]string{}, rev: map[string]string{}, count: map[string]int{}}
}

func (m *Masker) token(kind, orig string) string {
	if p, ok := m.fwd[orig]; ok {
		return p
	}
	m.count[kind]++
	p := "{{" + kind + "_" + itoa(m.count[kind]) + "}}"
	m.fwd[orig] = p
	m.rev[p] = orig
	return p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Mask 는 text 의 개인정보를 자리표시자로 바꾼다. 같은 값은 같은 자리표시자가 된다.
func (m *Masker) Mask(text string) string {
	for _, t := range m.terms {
		if strings.Contains(text, t.value) {
			text = strings.ReplaceAll(text, t.value, m.token(t.kind, t.value))
		}
	}
	for _, d := range detectors {
		kind := d.kind
		text = d.re.ReplaceAllStringFunc(text, func(s string) string { return m.token(kind, s) })
	}
	return text
}

// Unmask 는 자리표시자를 원문으로 되돌린다. 모델이 건드린 자리표시자는
// 되돌리지 못하고 그대로 남으므로, 남은 것을 Leftover 로 확인한다.
func (m *Masker) Unmask(text string) string {
	// 긴 자리표시자부터 되돌려 {{TERM_1}} 이 {{TERM_10}} 의 앞부분으로 잘못 매칭되지 않게 한다.
	keys := make([]string, 0, len(m.rev))
	for k := range m.rev {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		text = strings.ReplaceAll(text, k, m.rev[k])
	}
	return text
}

// Legend 는 지금까지 만든 자리표시자의 종류 설명이다. 원문 값은 담지 않는다.
// 요청 글을 다 만든 뒤 마지막에 붙여 AI 에게 알려 준다. 만든 것이 없으면 빈 문자열이다.
func (m *Masker) Legend() string {
	if len(m.rev) == 0 {
		return ""
	}
	toks := make([]string, 0, len(m.rev))
	for t := range m.rev {
		toks = append(toks, t)
	}
	sort.Strings(toks)
	var b strings.Builder
	b.WriteString("<자리표시자_설명>\n")
	for _, t := range toks {
		kind := strings.TrimSuffix(strings.TrimPrefix(t, "{{"), "}}")
		kind = kind[:strings.LastIndex(kind, "_")]
		b.WriteString(t + ": " + kindHint[kind] + "\n")
	}
	b.WriteString("</자리표시자_설명>\n")
	return b.String()
}

var leftoverRe = regexp.MustCompile(`\{\{[A-Z]+_\d+\}\}`)

// Leftover 는 Unmask 후에도 남은 자리표시자 목록이다.
func Leftover(text string) []string {
	return leftoverRe.FindAllString(text, -1)
}

// Finding 은 본문에서 발견한 개인정보 한 건이다. 값 자체는 담지 않는다.
type Finding struct {
	Kind  string
	Label string
	Count int
}

// Scan 은 text 에 개인정보로 보이는 값이 있는지 센다. 가림은 하지 않는다.
func Scan(text string) []Finding {
	var out []Finding
	remaining := text
	for _, d := range detectors {
		matches := d.re.FindAllString(remaining, -1)
		if len(matches) == 0 {
			continue
		}
		out = append(out, Finding{Kind: d.kind, Label: kindLabel[d.kind], Count: len(matches)})
		// 한 번 센 값은 지워서 다른 패턴이 같은 값을 또 세지 않게 한다.
		remaining = d.re.ReplaceAllString(remaining, " ")
	}
	return out
}
