// Package minwon 은 민원 초안 작성, 답변 평가, 재민원 작성, 문체 학습, 제출 전 점검을 담당한다.
package minwon

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"minwon-bot/internal/llm"
	"minwon-bot/internal/privacy"
)

//go:embed prompts/*.txt
var promptFS embed.FS

func prompt(name string) string {
	b, err := promptFS.ReadFile("prompts/" + name + ".txt")
	if err != nil {
		panic(err) // 빌드에 포함된 파일이므로 없을 수 없다
	}
	return string(b)
}

// Service 는 AI 호출에 필요한 것을 묶는다. 호출마다 새로 만들어 써도 된다.
type Service struct {
	LLM       llm.LLM
	MaskTerms []string
}

func New(l llm.LLM, maskTerms []string) *Service { return &Service{LLM: l, MaskTerms: maskTerms} }

var (
	ErrNothingToFollowUp = errors.New("재민원이 필요한 항목이 없습니다(모든 항목이 충분히 답변됨)")
	ErrNoAsks            = errors.New("질의 항목이 없어 평가할 수 없습니다")
	ErrEmptyAnswer       = errors.New("답변 본문이 비어 있습니다")
)

// ---------------------------------------------------------------- 초안

type QA struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type DraftInput struct {
	Title, Agency, Facts, Desired string
	Extra                         []QA // 되묻기에 대한 답
	Style                         *StyleProfile
}

type DraftResult struct {
	NeedsInfo        bool     `json:"needs_info"`
	Questions        []string `json:"questions"`
	Title            string   `json:"title"`
	Body             string   `json:"body"`
	Asks             []string `json:"asks"`
	SuggestedAgency  string   `json:"suggested_agency"`
	Visibility       string   `json:"visibility"`
	VisibilityReason string   `json:"visibility_reason"`
	Warnings         []string `json:"warnings"`
}

var draftSchema = llm.Obj(map[string]any{
	"needs_info":        llm.Bool(),
	"questions":         llm.StrArr(),
	"title":             llm.Str(),
	"body":              llm.Str(),
	"asks":              llm.StrArr(),
	"suggested_agency":  llm.Str(),
	"visibility":        llm.Enum("public", "private"),
	"visibility_reason": llm.Str(),
	"warnings":          llm.StrArr(),
})

// Draft 는 사안으로 초안을 만든다. 정보가 모자라면 NeedsInfo=true 와 되묻기 질문을 돌려준다.
func (s *Service) Draft(ctx context.Context, in DraftInput) (DraftResult, error) {
	if strings.TrimSpace(in.Facts) == "" {
		return DraftResult{}, errors.New("사실관계를 입력하세요")
	}
	m := privacy.NewMasker(s.MaskTerms)
	var b strings.Builder
	b.WriteString("<사안>\n")
	writeField(&b, m, "메모용 제목", in.Title)
	writeField(&b, m, "대상 기관", in.Agency)
	writeField(&b, m, "사실관계", in.Facts)
	writeField(&b, m, "원하는 결과", in.Desired)
	b.WriteString("</사안>\n")
	writeExtra(&b, m, in.Extra)
	writeStyle(&b, in.Style)
	b.WriteString(m.Legend())
	b.WriteString("\n위 내용으로 민원 초안을 작성하세요.")

	res, err := s.generateDraft(ctx, "draft", b.String(), m)
	if err != nil {
		return res, err
	}
	source := in.Title + "\n" + in.Agency + "\n" + in.Facts + "\n" + in.Desired
	for _, qa := range in.Extra {
		source += "\n" + qa.Question + "\n" + qa.Answer
	}
	res.Warnings = append(res.Warnings, groundingWarnings(source, res)...)
	return res, nil
}

func (s *Service) generateDraft(ctx context.Context, promptName, user string, m *privacy.Masker) (DraftResult, error) {
	raw, err := s.LLM.Generate(ctx, llm.Request{System: prompt(promptName), User: user, Schema: draftSchema})
	if err != nil {
		return DraftResult{}, err
	}
	res, err := llm.Decode[DraftResult](raw)
	if err != nil {
		return res, err
	}
	if res.NeedsInfo {
		res.Questions = cleanList(res.Questions)
		if len(res.Questions) == 0 {
			return res, errors.New("AI 가 정보가 부족하다고 했지만 질문을 주지 않았습니다. 다시 시도하세요")
		}
		if len(res.Questions) > 5 {
			res.Questions = res.Questions[:5]
		}
		res.Title, res.Body, res.Asks = "", "", nil
		return unmaskDraft(res, m), nil
	}
	res.Questions = nil
	res.Asks = cleanList(res.Asks)
	if strings.TrimSpace(res.Title) == "" || strings.TrimSpace(res.Body) == "" || len(res.Asks) == 0 {
		return res, errors.New("AI 가 제목, 본문, 질의 항목 중 일부가 빠진 초안을 돌려줬습니다. 다시 시도하세요")
	}
	res.Title = strings.TrimSpace(stripMarkdown(res.Title))
	res.Body = strings.TrimSpace(stripMarkdown(res.Body))
	if res.Visibility != "public" {
		res.Visibility = "private"
	}
	res = unmaskDraft(res, m)

	// 모델이 자리표시자를 건드려 복원하지 못한 경우 사용자가 직접 채우도록 알린다.
	left := privacy.Leftover(res.Title + res.Body + strings.Join(res.Asks, ""))
	if len(left) > 0 {
		res.Warnings = append(res.Warnings,
			"가려 둔 값이 복원되지 않은 자리가 있습니다("+strings.Join(uniq(left), ", ")+"). 직접 채워 넣으세요.")
	}
	return res, nil
}

func unmaskDraft(r DraftResult, m *privacy.Masker) DraftResult {
	r.Title = m.Unmask(r.Title)
	r.Body = m.Unmask(r.Body)
	r.SuggestedAgency = m.Unmask(r.SuggestedAgency)
	r.VisibilityReason = m.Unmask(r.VisibilityReason)
	r.Asks = mapAll(r.Asks, m.Unmask)
	r.Questions = mapAll(r.Questions, m.Unmask)
	r.Warnings = mapAll(r.Warnings, m.Unmask)
	return r
}

var (
	mdBold   = regexp.MustCompile(`\*\*|__`)
	mdHeader = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	mdTick   = regexp.MustCompile("`+")
)

// stripMarkdown 은 국민신문고 본문란에 그대로 들어가면 보기 흉한 마크다운 기호를 지운다.
func stripMarkdown(s string) string {
	s = mdBold.ReplaceAllString(s, "")
	s = mdHeader.ReplaceAllString(s, "")
	return mdTick.ReplaceAllString(s, "")
}

var numRe = regexp.MustCompile(`\d[\d,.]*\d|\d`)

// 숫자 바로 뒤에 붙으면 법령 조문 번호로 보고 검사에서 뺀다(제23조 등).
const lawSuffix = "조항호장절목"

// groundingWarnings 는 초안에 있는 두 자리 이상의 숫자가 입력에 없으면 알려 준다.
// 날짜, 인원, 금액을 AI 가 지어낸 경우를 잡기 위한 장치이며, 법령 조문 번호는 제외한다.
func groundingWarnings(source string, r DraftResult) []string {
	srcNorm := strings.ReplaceAll(source, ",", "")
	text := r.Title + "\n" + r.Body + "\n" + strings.Join(r.Asks, "\n")
	var bad []string
	for _, loc := range numRe.FindAllStringIndex(text, -1) {
		tok := text[loc[0]:loc[1]]
		norm := strings.Trim(strings.ReplaceAll(tok, ",", ""), ".")
		if len(norm) < 2 {
			continue
		}
		if rest := []rune(text[loc[1]:]); len(rest) > 0 && strings.ContainsRune(lawSuffix, rest[0]) {
			continue
		}
		if !strings.Contains(srcNorm, norm) {
			bad = append(bad, tok)
		}
	}
	bad = uniq(bad)
	if len(bad) == 0 {
		return nil
	}
	return []string{"입력하신 내용에 없는 숫자가 초안에 있습니다(" + strings.Join(bad, ", ") + "). 사실과 맞는지 확인하세요."}
}

// ---------------------------------------------------------------- 평가

type ItemEval struct {
	Ask        string `json:"ask"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
	Evidence   string `json:"evidence"`
	Confidence string `json:"confidence"`
}

type LegalRef struct {
	Name      string `json:"name"`
	Article   string `json:"article"`
	Relevance string `json:"relevance"`
	// URL 은 코드가 붙인다. 모델 출력에는 없다.
	URL string `json:"url,omitempty"`
}

type EvalResult struct {
	Items                []ItemEval `json:"items"`
	Score                int        `json:"score"`
	Summary              string     `json:"summary"`
	Recommendation       string     `json:"recommendation"`
	RecommendationReason string     `json:"recommendation_reason"`
	FollowUpFocus        []string   `json:"follow_up_focus"`
	OtherRoutes          []string   `json:"other_routes"`
	LegalRefs            []LegalRef `json:"legal_refs"`
	// Notes 는 코드가 덧붙이는 주의 사항이다. 모델 출력에는 없다.
	Notes []string `json:"notes,omitempty"`
}

const (
	VerdictSufficient   = "sufficient"
	VerdictPartial      = "partial"
	VerdictEvasive      = "evasive"
	VerdictUnanswered   = "unanswered"
	VerdictUndetermined = "undetermined"

	RecClose      = "close"
	RecFollowUp   = "follow_up"
	RecOtherRoute = "other_route"
	RecHold       = "hold"
)

var verdicts = []string{VerdictSufficient, VerdictPartial, VerdictEvasive, VerdictUnanswered, VerdictUndetermined}

var evalSchema = llm.Obj(map[string]any{
	"items": llm.Arr(llm.Obj(map[string]any{
		"ask":        llm.Str(),
		"verdict":    llm.Enum(verdicts...),
		"reason":     llm.Str(),
		"evidence":   llm.Str(),
		"confidence": llm.Enum("high", "medium", "low"),
	})),
	"score":                 llm.Int(),
	"summary":               llm.Str(),
	"recommendation":        llm.Enum(RecClose, RecFollowUp, RecOtherRoute, RecHold),
	"recommendation_reason": llm.Str(),
	"follow_up_focus":       llm.StrArr(),
	"other_routes":          llm.StrArr(),
	"legal_refs": llm.Arr(llm.Obj(map[string]any{
		"name": llm.Str(), "article": llm.Str(), "relevance": llm.Str(),
	})),
})

type EvalInput struct {
	Title, Body string
	Asks        []string
	Answer      string
	Dept        string
	Round       int
}

// Evaluate 는 답변이 질의 항목마다 답했는지 판정한다.
func (s *Service) Evaluate(ctx context.Context, in EvalInput) (EvalResult, error) {
	asks := cleanList(in.Asks)
	if len(asks) == 0 {
		return EvalResult{}, ErrNoAsks
	}
	if strings.TrimSpace(in.Answer) == "" {
		return EvalResult{}, ErrEmptyAnswer
	}
	m := privacy.NewMasker(s.MaskTerms)
	var b strings.Builder
	b.WriteString("<제출한_민원>\n")
	writeField(&b, m, "회차", fmt.Sprint(max(in.Round, 1)))
	writeField(&b, m, "제목", in.Title)
	writeField(&b, m, "본문", in.Body)
	b.WriteString("</제출한_민원>\n<asks>\n")
	for i, a := range asks {
		fmt.Fprintf(&b, "%d. %s\n", i+1, m.Mask(a))
	}
	b.WriteString("</asks>\n<답변>\n")
	writeField(&b, m, "답변 부서", in.Dept)
	b.WriteString(m.Mask(in.Answer))
	b.WriteString("\n</답변>\n")
	b.WriteString(m.Legend())
	b.WriteString("\n위 답변을 항목별로 판정하세요.")

	raw, err := s.LLM.Generate(ctx, llm.Request{System: prompt("evaluate"), User: b.String(), Schema: evalSchema})
	if err != nil {
		return EvalResult{}, err
	}
	res, err := llm.Decode[EvalResult](raw)
	if err != nil {
		return res, err
	}
	res = normalizeEval(res, asks)
	res.Summary = m.Unmask(res.Summary)
	res.RecommendationReason = m.Unmask(res.RecommendationReason)
	for i := range res.Items {
		res.Items[i].Reason = m.Unmask(res.Items[i].Reason)
		res.Items[i].Evidence = m.Unmask(res.Items[i].Evidence)
	}
	res.FollowUpFocus = mapAll(res.FollowUpFocus, m.Unmask)
	res.OtherRoutes = mapAll(res.OtherRoutes, m.Unmask)
	// 근거 문장이 실제 답변에 있는지 확인한다(모델이 인용을 지어냈는지).
	for i := range res.Items {
		if ev := strings.TrimSpace(res.Items[i].Evidence); ev != "" && !quoteInText(ev, in.Answer) {
			res.Items[i].Evidence += " (⚠ 답변 원문과 일치하지 않는 인용일 수 있음)"
		}
	}
	return res, nil
}

// normalizeEval 은 모델 출력을 코드로 한 번 더 점검한다. 항목 수와 순서를 질의에 맞추고,
// 근거 없이 종결을 권하지 않도록 권고를 보정한다.
func normalizeEval(r EvalResult, asks []string) EvalResult {
	items := make([]ItemEval, len(asks))
	for i, ask := range asks {
		if i < len(r.Items) {
			items[i] = r.Items[i]
		} else {
			items[i] = ItemEval{Verdict: VerdictUndetermined, Confidence: "low",
				Reason: "AI 가 이 항목을 평가하지 않았습니다."}
			r.Notes = append(r.Notes, fmt.Sprintf("질의 %d번은 AI 가 평가하지 않아 '판정 불가'로 처리했습니다.", i+1))
		}
		items[i].Ask = ask
		if !contains(verdicts, items[i].Verdict) {
			items[i].Verdict = VerdictUndetermined
		}
		if !contains([]string{"high", "medium", "low"}, items[i].Confidence) {
			items[i].Confidence = "low"
		}
	}
	if len(r.Items) > len(asks) {
		r.Notes = append(r.Notes, "AI 가 질의 항목보다 많은 판정을 돌려줘 초과분은 버렸습니다.")
	}
	r.Items = items
	r.Score = min(max(r.Score, 0), 100)

	var unresolved, shaky int
	for _, it := range items {
		switch it.Verdict {
		case VerdictPartial, VerdictEvasive, VerdictUnanswered:
			unresolved++
		}
		if it.Verdict == VerdictUndetermined || it.Confidence == "low" {
			shaky++
		}
	}
	if !contains([]string{RecClose, RecFollowUp, RecOtherRoute, RecHold}, r.Recommendation) {
		r.Recommendation = RecHold
	}
	switch {
	case r.Recommendation == RecClose && unresolved > 0:
		r.Recommendation = RecFollowUp
		r.Notes = append(r.Notes, "미흡한 항목이 남아 있어 '종결' 권고를 '재민원'으로 바꿨습니다.")
	case r.Recommendation == RecClose && shaky*2 >= len(items):
		r.Recommendation = RecHold
		r.Notes = append(r.Notes, "확신이 낮은 판정이 절반 이상이라 '종결' 대신 '판단 보류'로 바꿨습니다.")
	case r.Recommendation == RecFollowUp && unresolved == 0:
		r.Recommendation = RecHold
		r.Notes = append(r.Notes, "미흡한 항목이 없어 '재민원' 대신 '판단 보류'로 바꿨습니다.")
	}
	r.FollowUpFocus = cleanList(r.FollowUpFocus)
	r.OtherRoutes = cleanList(r.OtherRoutes)

	for i := range r.LegalRefs {
		name := strings.ReplaceAll(strings.TrimSpace(r.LegalRefs[i].Name), " ", "")
		if name != "" {
			r.LegalRefs[i].URL = "https://www.law.go.kr/법령/" + url.PathEscape(name)
		}
	}
	if len(r.LegalRefs) > 0 {
		r.Notes = append(r.Notes, "법령 인용은 AI 가 제시한 것이라 확인되지 않았습니다. 국가법령정보센터에서 조문을 확인하세요.")
	}
	return r
}

// Unresolved 는 재민원 대상이 되는(충분히 답변되지 않은) 질의 항목이다.
func (r EvalResult) Unresolved() []ItemEval {
	var out []ItemEval
	for _, it := range r.Items {
		switch it.Verdict {
		case VerdictPartial, VerdictEvasive, VerdictUnanswered:
			out = append(out, it)
		}
	}
	return out
}

var spaceRe = regexp.MustCompile(`\s+`)

func quoteInText(quote, text string) bool {
	q := spaceRe.ReplaceAllString(strings.Trim(quote, " \"'“”‘’…"), "")
	t := spaceRe.ReplaceAllString(text, "")
	q = strings.TrimSuffix(q, "...")
	return q != "" && strings.Contains(t, q)
}

// ---------------------------------------------------------------- 재민원

type FollowUpInput struct {
	Agency       string
	Round        int // 새로 낼 재민원의 회차
	PrevReceipt  string
	PrevSubmitAt string
	PrevTitle    string
	PrevBody     string
	PrevAsks     []string
	Answer       string
	Dept         string
	Eval         EvalResult
	Style        *StyleProfile
}

// FollowUp 은 미흡하게 답변된 항목만 겨냥한 재민원 초안을 만든다.
func (s *Service) FollowUp(ctx context.Context, in FollowUpInput) (DraftResult, error) {
	unresolved := in.Eval.Unresolved()
	if len(unresolved) == 0 {
		return DraftResult{}, ErrNothingToFollowUp
	}
	m := privacy.NewMasker(s.MaskTerms)
	var b strings.Builder
	b.WriteString("<이전_민원>\n")
	writeField(&b, m, "대상 기관", in.Agency)
	writeField(&b, m, "회차", fmt.Sprint(in.Round-1))
	writeField(&b, m, "접수번호", in.PrevReceipt)
	writeField(&b, m, "제출 시기", in.PrevSubmitAt)
	writeField(&b, m, "제목", in.PrevTitle)
	writeField(&b, m, "본문", in.PrevBody)
	b.WriteString("</이전_민원>\n<답변>\n")
	writeField(&b, m, "답변 부서", in.Dept)
	b.WriteString(m.Mask(in.Answer))
	b.WriteString("\n</답변>\n<평가>\n")
	for _, it := range in.Eval.Items {
		fmt.Fprintf(&b, "- [%s] %s / 이유: %s\n", it.Verdict, m.Mask(it.Ask), m.Mask(it.Reason))
	}
	for _, f := range in.Eval.FollowUpFocus {
		fmt.Fprintf(&b, "- 다시 물을 쟁점: %s\n", m.Mask(f))
	}
	b.WriteString("</평가>\n")
	fmt.Fprintf(&b, "<새_회차>%d</새_회차>\n", in.Round)
	writeStyle(&b, in.Style)
	b.WriteString(m.Legend())
	b.WriteString("\n위 내용으로 재민원 초안을 작성하세요.")

	res, err := s.generateDraft(ctx, "followup", b.String(), m)
	if err != nil {
		return res, err
	}
	if res.NeedsInfo {
		return res, errors.New("AI 가 재민원에 필요한 정보가 부족하다고 답했습니다. 다시 시도하세요")
	}
	source := in.PrevTitle + "\n" + in.PrevBody + "\n" + in.Answer + "\n" + in.PrevReceipt + "\n" + in.PrevSubmitAt
	for _, it := range in.Eval.Items {
		source += "\n" + it.Ask + "\n" + it.Reason + "\n" + it.Evidence
	}
	res.Warnings = append(res.Warnings, groundingWarnings(source, res)...)
	return res, nil
}

// ---------------------------------------------------------------- 문체

type StyleProfile struct {
	Summary         string   `json:"summary"`
	Tone            string   `json:"tone"`
	SentenceLength  string   `json:"sentence_length"`
	SelfReference   string   `json:"self_reference"`
	OfficialAddress string   `json:"official_address"`
	Numbering       string   `json:"numbering"`
	Opening         string   `json:"opening"`
	Closing         string   `json:"closing"`
	Phrases         []string `json:"phrases"`
	Avoid           []string `json:"avoid"`
}

// Empty 는 아직 학습된 문체가 없다는 뜻이다.
func (p StyleProfile) Empty() bool { return strings.TrimSpace(p.Summary) == "" }

// Prompt 는 작성 프롬프트에 넣을 문체 지침 텍스트다.
func (p StyleProfile) Prompt() string {
	var b strings.Builder
	line := func(label, v string) {
		if v = strings.TrimSpace(v); v != "" {
			fmt.Fprintf(&b, "- %s: %s\n", label, v)
		}
	}
	line("요약 지침", p.Summary)
	line("어조", p.Tone)
	line("문장 길이", p.SentenceLength)
	line("자기 지칭", p.SelfReference)
	line("기관·담당자 호칭", p.OfficialAddress)
	line("번호 매기기", p.Numbering)
	line("첫머리", p.Opening)
	line("마무리", p.Closing)
	if len(p.Phrases) > 0 {
		line("자주 쓰는 표현", strings.Join(p.Phrases, " / "))
	}
	if len(p.Avoid) > 0 {
		line("쓰지 않는 표현", strings.Join(p.Avoid, " / "))
	}
	return b.String()
}

type StyleText struct {
	Text     string
	Original string // 비어 있지 않으면 AI 초안이고 Text 는 사용자가 고친 최종본이다
}

var styleSchema = llm.Obj(map[string]any{
	"summary":          llm.Str(),
	"tone":             llm.Str(),
	"sentence_length":  llm.Str(),
	"self_reference":   llm.Str(),
	"official_address": llm.Str(),
	"numbering":        llm.Str(),
	"opening":          llm.Str(),
	"closing":          llm.Str(),
	"phrases":          llm.StrArr(),
	"avoid":            llm.StrArr(),
})

const maxStyleChars = 3000

// ExtractStyle 은 사용자가 쓴 글과 초안 수정 사례에서 문체 프로필을 뽑는다.
func (s *Service) ExtractStyle(ctx context.Context, texts []StyleText) (StyleProfile, error) {
	var samples, edits []StyleText
	for _, t := range texts {
		if strings.TrimSpace(t.Text) == "" {
			continue
		}
		if strings.TrimSpace(t.Original) != "" {
			edits = append(edits, t)
		} else {
			samples = append(samples, t)
		}
	}
	if len(samples) == 0 && len(edits) == 0 {
		return StyleProfile{}, errors.New("문체를 학습할 글이 없습니다. 직접 쓴 글을 붙여넣으세요")
	}
	m := privacy.NewMasker(s.MaskTerms)
	var b strings.Builder
	for i, t := range samples {
		fmt.Fprintf(&b, "<글 번호=\"%d\">\n%s\n</글>\n", i+1, clip(m.Mask(t.Text), maxStyleChars))
	}
	for i, t := range edits {
		fmt.Fprintf(&b, "<수정사례 번호=\"%d\">\n[AI 초안]\n%s\n[사용자 최종본]\n%s\n</수정사례>\n",
			i+1, clip(m.Mask(t.Original), maxStyleChars), clip(m.Mask(t.Text), maxStyleChars))
	}
	b.WriteString(m.Legend())
	b.WriteString("\n위 글에서 문체 프로필을 뽑으세요.")

	raw, err := s.LLM.Generate(ctx, llm.Request{System: prompt("style"), User: b.String(), Schema: styleSchema})
	if err != nil {
		return StyleProfile{}, err
	}
	p, err := llm.Decode[StyleProfile](raw)
	if err != nil {
		return p, err
	}
	p = scrubTokens(p)
	if p.Empty() {
		return p, errors.New("AI 가 문체 요약을 만들지 못했습니다. 글을 더 붙여넣고 다시 시도하세요")
	}
	return p, nil
}

var tokenRe = regexp.MustCompile(`\{\{[A-Z]+_\d+\}\}`)

// scrubTokens 는 프로필에 섞여 들어온 자리표시자를 없앤다. 자리표시자는 호출마다
// 대응 값이 달라서, 저장된 프로필에 남아 있으면 다음 호출에서 엉뚱한 값으로 복원될 수 있다.
func scrubTokens(p StyleProfile) StyleProfile {
	clean := func(s string) string { return strings.TrimSpace(tokenRe.ReplaceAllString(s, "(가려진 값)")) }
	dropTokens := func(in []string) []string {
		var out []string
		for _, s := range cleanList(in) {
			if !tokenRe.MatchString(s) {
				out = append(out, s)
			}
		}
		return out
	}
	p.Summary, p.Tone, p.SentenceLength = clean(p.Summary), clean(p.Tone), clean(p.SentenceLength)
	p.SelfReference, p.OfficialAddress, p.Numbering = clean(p.SelfReference), clean(p.OfficialAddress), clean(p.Numbering)
	p.Opening, p.Closing = clean(p.Opening), clean(p.Closing)
	p.Phrases, p.Avoid = dropTokens(p.Phrases), dropTokens(p.Avoid)
	return p
}

// ParseStyle 은 저장된 JSON 을 프로필로 읽는다.
func ParseStyle(js string) (*StyleProfile, error) {
	if strings.TrimSpace(js) == "" {
		return nil, nil
	}
	var p StyleProfile
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		return nil, err
	}
	if p.Empty() {
		return nil, nil
	}
	return &p, nil
}

// ---------------------------------------------------------------- 공통 도우미

func writeField(b *strings.Builder, m *privacy.Masker, label, v string) {
	if v = strings.TrimSpace(v); v == "" {
		return
	}
	fmt.Fprintf(b, "%s: %s\n", label, m.Mask(v))
}

func writeExtra(b *strings.Builder, m *privacy.Masker, qa []QA) {
	var any bool
	for _, x := range qa {
		if strings.TrimSpace(x.Answer) == "" {
			continue
		}
		if !any {
			b.WriteString("<추가_답변>\n")
			any = true
		}
		fmt.Fprintf(b, "Q: %s\nA: %s\n", m.Mask(x.Question), m.Mask(x.Answer))
	}
	if any {
		b.WriteString("</추가_답변>\n")
	}
}

func writeStyle(b *strings.Builder, p *StyleProfile) {
	if p == nil || p.Empty() {
		return
	}
	b.WriteString("<문체_프로필>\n" + p.Prompt() + "</문체_프로필>\n")
}

func cleanList(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func mapAll(in []string, f func(string) string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
