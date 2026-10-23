package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/minwon"
	"minwon-bot/internal/store"
)

type casesPage struct {
	base
	Cases  []store.Case
	AutoOn bool
}

func (s *Server) pageCases(w http.ResponseWriter, r *http.Request) {
	cases, err := s.d.Store.ListCases(r.Context())
	if err != nil {
		http.Error(w, "목록을 읽지 못했습니다", http.StatusInternalServerError)
		return
	}
	s.render(w, "cases", casesPage{base: s.base(r, "사안"), Cases: cases, AutoOn: s.config().Auto.Enabled})
}

func (s *Server) pageCaseNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, "case_new", s.base(r, "새 사안"))
}

func (s *Server) postCase(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "요청 형식이 올바르지 않습니다", http.StatusBadRequest)
		return
	}
	c := store.Case{
		Title:   strings.TrimSpace(r.PostForm.Get("title")),
		Agency:  strings.TrimSpace(r.PostForm.Get("agency")),
		Facts:   strings.TrimSpace(r.PostForm.Get("facts")),
		Desired: strings.TrimSpace(r.PostForm.Get("desired")),
	}
	if c.Facts == "" {
		http.Error(w, "사실관계를 입력하세요", http.StatusBadRequest)
		return
	}
	if c.Title == "" {
		c.Title = strings.Join(strings.Fields(truncRunes(c.Facts, 30)), " ")
	}
	id, err := s.d.Store.CreateCase(r.Context(), c)
	if err != nil {
		http.Error(w, "사안을 저장하지 못했습니다", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/cases/"+itoa(id), http.StatusSeeOther)
}

type answerView struct {
	A    store.Answer
	Eval *minwon.EvalResult
	// Waiting 은 자동 처리를 기다리는 중이라는 뜻이다.
	Waiting bool
}

type subView struct {
	S        store.Submission
	Asks     []string
	Draft    *minwon.DraftResult
	Findings []minwon.Finding
	Answers  []answerView
}

type casePage struct {
	base
	Case        store.Case
	Subs        []subView
	Events      []store.Event
	HasStyle    bool
	CanFollowUp bool
	AutoOn      bool
	// Polling 이 true 면 화면이 자동 처리 결과를 기다리며 주기적으로 새로 고친다.
	Polling bool
}

func (s *Server) pageCase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	c, err := s.d.Store.GetCase(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	subs, err := s.d.Store.ListSubmissions(ctx, id)
	if err != nil {
		http.Error(w, "제출 기록을 읽지 못했습니다", http.StatusInternalServerError)
		return
	}
	cfg := s.config()
	autoOn := cfg.Auto.Enabled && cfg.Auto.AnswerAuto
	pg := casePage{base: s.base(r, c.Title), Case: c, HasStyle: s.loadStyle(ctx) != nil, AutoOn: autoOn}
	hasDraft := false
	for _, sub := range subs {
		v := subView{S: sub, Asks: parseAsks(sub.AsksJSON)}
		if sub.DraftJSON != "" {
			var d minwon.DraftResult
			if json.Unmarshal([]byte(sub.DraftJSON), &d) == nil {
				v.Draft = &d
			}
		}
		if sub.Status == "draft" {
			hasDraft = true
			v.Findings = s.findings(ctx, sub, subs)
		}
		answers, _ := s.d.Store.ListAnswers(ctx, sub.ID)
		for _, a := range answers {
			av := answerView{A: a}
			av.Waiting = autoOn && a.ProcessedAt.IsZero() && c.Status == "open" && !c.AutoOff
			// 방금 기록한 답변이 처리되기를 기다리는 동안만 새로 고친다(오래 걸리면 멈춘다).
			if av.Waiting && s.d.Now().Sub(a.ReceivedAt) < 10*time.Minute {
				pg.Polling = true
			}
			if ev, err := s.d.Store.LatestEvaluation(ctx, a.ID); err == nil {
				var res minwon.EvalResult
				if json.Unmarshal([]byte(ev.ResultJSON), &res) == nil {
					av.Eval = &res
				}
			}
			v.Answers = append(v.Answers, av)
		}
		pg.Subs = append(pg.Subs, v)
	}
	pg.CanFollowUp = !hasDraft && c.Status == "open"
	pg.Events, _ = s.d.Store.ListEvents(ctx, id)
	s.render(w, "case", pg)
}

type settingsPage struct {
	base
	Cfg     config.Config // 비밀 값은 비워서 전달한다
	Efforts []string
	Style   *minwon.StyleProfile
	Samples []store.StyleSample
	// 비밀 값은 화면에 내려 보내지 않고, 저장되어 있는지만 알려 준다.
	HasTelegramToken, HasEmailPassword bool
	TelegramEnv, EmailEnv              bool // 환경변수로 제공 중
	PortalName                         string
}

func (s *Server) pageSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	samples, _ := s.d.Store.ListStyleSamples(ctx, "", 30)
	cfg := s.config()
	pg := settingsPage{
		base: s.base(r, "설정"), Efforts: []string{"low", "medium", "high", "xhigh", "max"},
		Style: s.loadStyle(ctx), Samples: samples,
		HasTelegramToken: cfg.Report.Telegram.Token != "", HasEmailPassword: cfg.Report.Email.Password != "",
		TelegramEnv: os.Getenv("MINWON_TELEGRAM_TOKEN") != "", EmailEnv: os.Getenv("MINWON_SMTP_PASSWORD") != "",
	}
	cfg.Report.Telegram.Token, cfg.Report.Email.Password = "", ""
	pg.Cfg = cfg
	if s.d.Portal != nil {
		pg.PortalName = s.d.Portal.Name()
	}
	s.render(w, "settings", pg)
}

type inboxPage struct {
	base
	Reports []store.Report
}

func (s *Server) pageInbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reps, err := s.d.Store.ListReports(ctx, 100)
	if err != nil {
		http.Error(w, "보고함을 읽지 못했습니다", http.StatusInternalServerError)
		return
	}
	// 이 화면에서 새 보고를 강조해 보여 준 뒤 읽음 처리한다. 상단 숫자는 읽음 처리 뒤 값이다.
	_ = s.d.Store.MarkReportsRead(ctx)
	s.render(w, "inbox", inboxPage{base: s.base(r, "보고함"), Reports: reps})
}

// findings 는 초안 한 건의 제출 전 점검 결과다.
func (s *Server) findings(ctx context.Context, sub store.Submission, all []store.Submission) []minwon.Finding {
	var prev []string
	for _, o := range all {
		if o.ID != sub.ID && o.Round < sub.Round {
			prev = append(prev, o.Body)
		}
	}
	now := s.d.Now()
	n, _ := s.d.Store.CountSubmittedSince(ctx, now.Add(-24*time.Hour))
	last, _ := s.d.Store.LastSubmittedAt(ctx)
	return minwon.Check(minwon.GuardInput{
		Round: sub.Round, Title: sub.Title, Body: sub.Body, PrevBodies: prev,
		SubmittedLast24h: n, LastSubmittedAt: last, Now: now, Limits: s.config().Limits,
	})
}

func parseAsks(js string) []string {
	var asks []string
	_ = json.Unmarshal([]byte(js), &asks)
	return asks
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
