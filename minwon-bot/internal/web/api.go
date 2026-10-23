package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"minwon-bot/internal/config"
	"minwon-bot/internal/llm"
	"minwon-bot/internal/minwon"
	"minwon-bot/internal/report"
)

const (
	maxTextRunes = 60000
	maxStyleText = 20000
)

func cleanAsks(in []string) []string {
	var out []string
	for _, a := range in {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------------------------------------------------------------- 초안

func (s *Server) apiDraft(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Extra []minwon.QA `json:"extra"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	s.d.Lock.Lock()
	defer s.d.Lock.Unlock()
	ctx := r.Context()

	c, err := s.d.Store.GetCase(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if c.Status != "open" {
		s.fail(w, conflict("종결된 사안입니다"))
		return
	}
	subs, err := s.d.Store.ListSubmissions(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(subs) > 0 {
		s.fail(w, conflict("이미 초안이나 제출 기록이 있는 사안입니다"))
		return
	}
	// 되묻기에 답한 내용은 사실관계에 남겨 두어, 새로 고쳐도 다시 묻지 않게 한다.
	var qa []string
	for _, x := range req.Extra {
		if a := strings.TrimSpace(x.Answer); a != "" {
			qa = append(qa, "[추가 답변] "+strings.TrimSpace(x.Question)+" → "+a)
		}
	}
	if len(qa) > 0 {
		if err := s.d.Store.AppendCaseFacts(ctx, id, strings.Join(qa, "\n")); err != nil {
			s.fail(w, err)
			return
		}
		if c, err = s.d.Store.GetCase(ctx, id); err != nil {
			s.fail(w, err)
			return
		}
	}
	res, err := s.svc().Draft(ctx, minwon.DraftInput{
		Title: c.Title, Agency: c.Agency, Facts: c.Facts, Desired: c.Desired, Style: s.loadStyle(ctx),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if res.NeedsInfo {
		writeJSON(w, http.StatusOK, map[string]any{"needs_info": true, "questions": res.Questions})
		return
	}
	sub, err := s.d.Store.CreateSubmission(ctx, id, "initial", mustJSON(res), res.Title, res.Body, mustJSON(res.Asks))
	if err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, id, "draft_created", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "submission_id": sub.ID})
}

type textReq struct {
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Asks  []string `json:"asks"`
}

func (t textReq) validate() error {
	if len([]rune(t.Title)) > 300 || len([]rune(t.Body)) > maxTextRunes {
		return bad("제목이나 본문이 너무 깁니다")
	}
	return nil
}

func (s *Server) apiSave(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req textReq
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if err := req.validate(); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if err := s.d.Store.SaveSubmissionText(ctx, id, strings.TrimSpace(req.Title), strings.TrimSpace(req.Body), mustJSON(cleanAsks(req.Asks))); err != nil {
		s.fail(w, err)
		return
	}
	sub, err := s.d.Store.GetSubmission(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	all, _ := s.d.Store.ListSubmissions(ctx, sub.CaseID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "findings": s.findings(ctx, sub, all)})
}

func (s *Server) apiSubmitted(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		textReq
		ReceiptNo string `json:"receipt_no"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if err := req.validate(); err != nil {
		s.fail(w, err)
		return
	}
	receipt := strings.TrimSpace(req.ReceiptNo)
	if receipt == "" || len([]rune(receipt)) > 64 || strings.IndexFunc(receipt, unicode.IsControl) >= 0 {
		s.fail(w, bad("접수번호를 확인하세요"))
		return
	}
	ctx := r.Context()
	before, err := s.d.Store.GetSubmission(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	final := strings.TrimSpace(req.Body)
	if err := s.d.Store.SaveSubmissionText(ctx, id, strings.TrimSpace(req.Title), final, mustJSON(cleanAsks(req.Asks))); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.d.Store.MarkSubmitted(ctx, id, receipt); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, before.CaseID, "submitted", "접수번호 "+receipt)

	// 사용자가 AI 초안을 고쳐서 냈다면 그 차이가 문체 학습의 가장 좋은 재료다.
	var draft minwon.DraftResult
	if json.Unmarshal([]byte(before.DraftJSON), &draft) == nil && draft.Body != "" &&
		minwon.Similarity(draft.Body, final) < 0.98 {
		_ = s.d.Store.AddStyleSample(ctx, "edit", final, draft.Body)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiDiscard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	sub, err := s.d.Store.GetSubmission(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.d.Store.DeleteDraft(ctx, id); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, sub.CaseID, "discarded", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiClose(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len([]rune(reason)) > 300 {
		s.fail(w, bad("종결 사유를 1~300자로 적으세요"))
		return
	}
	ctx := r.Context()
	if err := s.d.Store.CloseCase(ctx, id, reason); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, id, "closed", reason)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- 답변, 평가, 재민원

func (s *Server) apiAnswerAdd(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Body string `json:"body"`
		Dept string `json:"dept"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	body, dept := strings.TrimSpace(req.Body), strings.TrimSpace(req.Dept)
	if body == "" || len([]rune(body)) > maxTextRunes || len([]rune(dept)) > 100 {
		s.fail(w, bad("답변 본문을 확인하세요"))
		return
	}
	ctx := r.Context()
	sub, err := s.d.Store.GetSubmission(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if sub.Status == "draft" {
		s.fail(w, conflict("아직 제출하지 않은 초안에는 답변을 기록할 수 없습니다"))
		return
	}
	if _, err := s.d.Store.CreateAnswer(ctx, id, body, dept); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, sub.CaseID, "answer_added", "")
	// 자동 처리가 켜져 있고 이 사안이 자동 처리 대상이면 승인 없이 평가하고 재민원 또는 종결까지 처리한다.
	cfg := s.config()
	c, _ := s.d.Store.GetCase(ctx, sub.CaseID)
	auto := cfg.Auto.Enabled && cfg.Auto.AnswerAuto && c.Status == "open" && !c.AutoOff
	if auto {
		go s.runner.ProcessPending(context.Background())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auto": auto})
}

func (s *Server) apiEvaluate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.d.Lock.Lock()
	defer s.d.Lock.Unlock()
	ctx := r.Context()
	ans, err := s.d.Store.GetAnswer(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	sub, err := s.d.Store.GetSubmission(ctx, ans.SubmissionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.svc().Evaluate(ctx, minwon.EvalInput{
		Title: sub.Title, Body: sub.Body, Asks: parseAsks(sub.AsksJSON),
		Answer: ans.Body, Dept: ans.Dept, Round: sub.Round,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.d.Store.SaveEvaluation(ctx, id, mustJSON(res), res.Score, res.Recommendation); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, sub.CaseID, "evaluated", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiFollowUp(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.d.Lock.Lock()
	defer s.d.Lock.Unlock()
	ctx := r.Context()
	ans, err := s.d.Store.GetAnswer(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	sub, err := s.d.Store.GetSubmission(ctx, ans.SubmissionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	c, err := s.d.Store.GetCase(ctx, sub.CaseID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if c.Status != "open" {
		s.fail(w, conflict("종결된 사안입니다"))
		return
	}
	all, err := s.d.Store.ListSubmissions(ctx, c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	round := 1
	for _, o := range all {
		if o.Status == "draft" {
			s.fail(w, conflict("작성 중인 초안이 이미 있습니다. 먼저 제출하거나 버리세요"))
			return
		}
		round = max(round, o.Round+1)
	}
	ev, err := s.d.Store.LatestEvaluation(ctx, id)
	if err != nil {
		s.fail(w, conflict("먼저 답변을 평가하세요"))
		return
	}
	var eval minwon.EvalResult
	if err := json.Unmarshal([]byte(ev.ResultJSON), &eval); err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.svc().FollowUp(ctx, minwon.FollowUpInput{
		Agency: c.Agency, Round: round, PrevReceipt: sub.ReceiptNo,
		PrevSubmitAt: sub.SubmittedAt.Local().Format("2006년 1월 2일"),
		PrevTitle:    sub.Title, PrevBody: sub.Body, PrevAsks: parseAsks(sub.AsksJSON),
		Answer: ans.Body, Dept: ans.Dept, Eval: eval, Style: s.loadStyle(ctx),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.d.Store.CreateSubmission(ctx, c.ID, "followup", mustJSON(res), res.Title, res.Body, mustJSON(res.Asks)); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, c.ID, "followup_created", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- 설정, 문체

func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := readJSON(r, &cfg); err != nil {
		s.fail(w, err)
		return
	}
	var terms []string
	seen := map[string]bool{}
	for _, t := range cfg.MaskTerms {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	cfg.MaskTerms = terms
	// 화면에는 비밀 값을 내려 보내지 않으므로, 비워서 보내면 저장된 값을 그대로 둔다.
	old := s.config()
	if cfg.Report.Telegram.Token == "" {
		cfg.Report.Telegram.Token = old.Report.Telegram.Token
	}
	if cfg.Report.Email.Password == "" {
		cfg.Report.Email.Password = old.Report.Email.Password
	}
	if err := cfg.Validate(); err != nil {
		s.fail(w, bad(err.Error()))
		return
	}
	if err := s.d.SaveConfig(cfg); err != nil {
		s.fail(w, err)
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.llm = s.d.NewLLM(cfg)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiLLMTest(w http.ResponseWriter, r *http.Request) {
	s.d.Lock.Lock()
	defer s.d.Lock.Unlock()
	s.mu.RLock()
	l := s.llm
	s.mu.RUnlock()
	start := s.d.Now()
	_, err := l.Generate(r.Context(), llm.Request{
		System: "JSON 으로만 답한다.",
		User:   "ok 를 true 로 답하세요.",
		Schema: llm.Obj(map[string]any{"ok": llm.Bool()}),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": l.Name(), "ms": s.d.Now().Sub(start).Milliseconds()})
}

func (s *Server) apiStyleAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len([]rune(text)) > maxStyleText {
		s.fail(w, bad("글을 1~20000자로 붙여넣으세요"))
		return
	}
	if err := s.d.Store.AddStyleSample(r.Context(), "sample", text, ""); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiStyleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.d.Store.DeleteStyleSample(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiStyleExtract(w http.ResponseWriter, r *http.Request) {
	s.d.Lock.Lock()
	defer s.d.Lock.Unlock()
	ctx := r.Context()
	samples, err := s.d.Store.ListStyleSamples(ctx, "sample", 5)
	if err != nil {
		s.fail(w, err)
		return
	}
	edits, err := s.d.Store.ListStyleSamples(ctx, "edit", 5)
	if err != nil {
		s.fail(w, err)
		return
	}
	var texts []minwon.StyleText
	for _, x := range samples {
		texts = append(texts, minwon.StyleText{Text: x.Text})
	}
	for _, x := range edits {
		texts = append(texts, minwon.StyleText{Text: x.Text, Original: x.Original})
	}
	p, err := s.svc().ExtractStyle(ctx, texts)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.d.Store.SetSetting(ctx, styleKey, mustJSON(p)); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiStyleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Store.SetSetting(r.Context(), styleKey, ""); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- 자동 처리, 보고

func (s *Server) apiReopen(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if err := s.d.Store.ReopenCase(ctx, id); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.d.Store.AddEvent(ctx, id, "reopened", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiCaseAuto(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Off bool `json:"off"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if err := s.d.Store.SetCaseAutoOff(ctx, id, req.Off); err != nil {
		s.fail(w, err)
		return
	}
	kind := "auto_on"
	if req.Off {
		kind = "auto_off"
	}
	_ = s.d.Store.AddEvent(ctx, id, kind, "")
	if !req.Off {
		go s.runner.ProcessPending(context.Background())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiAttentionClear(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.d.Store.SetAttention(r.Context(), id, ""); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiAutoRun 은 지금 한 번 점검한다. 시간이 걸릴 수 있어 백그라운드로 돌리고 결과는 보고함에 쌓인다.
func (s *Server) apiAutoRun(w http.ResponseWriter, r *http.Request) {
	if !s.config().Auto.Enabled {
		s.fail(w, conflict("설정에서 자동 처리가 꺼져 있습니다"))
		return
	}
	go s.runner.CheckNow(context.Background())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiReportTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel string `json:"channel"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	cfg := s.config()
	var rep report.Reporter
	switch req.Channel {
	case "telegram":
		t := report.TelegramFrom(cfg)
		if t == nil {
			s.fail(w, bad("텔레그램 토큰과 채팅 ID 를 먼저 저장하세요"))
			return
		}
		rep = t
	case "email":
		e := report.EmailFrom(cfg)
		if e == nil {
			s.fail(w, bad("이메일 서버와 주소를 먼저 저장하세요"))
			return
		}
		rep = e
	default:
		rep = report.Inbox{Store: s.d.Store}
	}
	err := rep.Send(r.Context(), report.Report{
		Kind: report.KindTest, Title: "보고 채널 시험", Summary: "이 메시지가 보이면 보고 채널이 정상입니다.",
		Body: "민원 도우미에서 보낸 시험 메시지입니다.", At: s.d.Now(),
	})
	if err != nil {
		s.fail(w, &userErr{http.StatusBadGateway, err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": rep.Name()})
}

// apiReportClear 는 텔레그램·이메일 설정(비밀 값 포함)을 지운다.
func (s *Server) apiReportClear(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	cfg.Report.Telegram = config.Telegram{}
	def := config.Default().Report.Email
	cfg.Report.Email = def
	if err := s.d.SaveConfig(cfg); err != nil {
		s.fail(w, err)
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
