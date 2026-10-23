// Package auto 는 답변이 기록된 뒤 사람 승인 없이 평가하고, 재민원을 쓰거나 사안을 종결하고,
// 그 결과를 사후보고하는 처리를 맡는다. 사이트 연동(portal)이 없으면 재민원은 초안까지만 만든다.
package auto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/minwon"
	"minwon-bot/internal/portal"
	"minwon-bot/internal/report"
	"minwon-bot/internal/store"
)

type Deps struct {
	Store *store.Store
	// Portal 이 nil 이면 사이트 연동이 없는 것이다.
	Portal   portal.Portal
	Service  func() *minwon.Service
	Config   func() config.Config
	Reporter func() report.Reporter
	Style    func(ctx context.Context) *minwon.StyleProfile
	Now      func() time.Time
	// Lock 은 AI 를 부르며 상태를 바꾸는 작업을 한 번에 하나만 하게 한다. 화면의 작업과 공유한다.
	Lock sync.Locker
}

type Runner struct {
	d Deps

	cycle sync.Mutex // 점검·처리 주기가 겹치지 않게
	mu    sync.Mutex
	fails map[int64]int // 답변별 연속 실패 횟수(메모리)
	last  time.Time     // 마지막 사이트 점검
}

const (
	maxFails = 3
	// 사이트 점검 간격과 무관하게 미뤄 둔 일과 하루 요약을 확인하는 주기.
	baseInterval = 10 * time.Minute
)

func New(d Deps) *Runner {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Lock == nil {
		d.Lock = &sync.Mutex{}
	}
	if d.Style == nil {
		d.Style = func(context.Context) *minwon.StyleProfile { return nil }
	}
	return &Runner{d: d, fails: map[int64]int{}}
}

// Start 는 ctx 가 끝날 때까지 주기적으로 Tick 을 부른다.
func (r *Runner) Start(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick(ctx)
			t.Reset(baseInterval)
		}
	}
}

// Tick 은 한 번의 점검이다: 사이트의 새 답변 확인(설정한 간격마다), 미처리 답변 처리, 하루 요약.
func (r *Runner) Tick(ctx context.Context) {
	cfg := r.d.Config()
	if !cfg.Auto.Enabled {
		return
	}
	if !r.cycle.TryLock() {
		return
	}
	defer r.cycle.Unlock()

	if r.d.Portal != nil && r.d.Now().Sub(r.last) >= time.Duration(cfg.Auto.CheckIntervalHours)*time.Hour {
		r.last = r.d.Now()
		r.checkPortal(ctx)
	}
	if cfg.Auto.AnswerAuto {
		r.processPending(ctx)
	}
	r.maybeDaily(ctx, cfg)
}

// CheckNow 는 사이트 점검 간격과 무관하게 지금 한 번 점검한다(화면의 "지금 확인").
func (r *Runner) CheckNow(ctx context.Context) {
	cfg := r.d.Config()
	if !cfg.Auto.Enabled {
		return
	}
	r.cycle.Lock()
	defer r.cycle.Unlock()
	if r.d.Portal != nil {
		r.last = r.d.Now()
		r.checkPortal(ctx)
	}
	if cfg.Auto.AnswerAuto {
		r.processPending(ctx)
	}
}

// ProcessPending 은 미처리 답변만 처리한다. 사용자가 답변을 붙여넣은 직후에 부른다.
func (r *Runner) ProcessPending(ctx context.Context) {
	cfg := r.d.Config()
	if !cfg.Auto.Enabled || !cfg.Auto.AnswerAuto {
		return
	}
	r.cycle.Lock()
	defer r.cycle.Unlock()
	r.processPending(ctx)
}

func (r *Runner) processPending(ctx context.Context) {
	answers, err := r.d.Store.ListPendingAnswers(ctx)
	if err != nil {
		log.Printf("미처리 답변을 읽지 못했습니다: %v", err)
		return
	}
	for _, a := range answers {
		if ctx.Err() != nil {
			return
		}
		r.ProcessAnswer(ctx, a)
	}
}

// ---------------------------------------------------------------- 사이트 점검

func (r *Runner) checkPortal(ctx context.Context) {
	subs, err := r.d.Store.ListAwaitingSubmissions(ctx)
	if err != nil {
		log.Printf("답변 대기 목록을 읽지 못했습니다: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	if err := r.d.Portal.EnsureLogin(ctx); err != nil {
		r.portalProblem(ctx, err)
		return
	}
	problem := false
	for _, sub := range subs {
		if ctx.Err() != nil {
			return
		}
		ans, err := r.d.Portal.FetchAnswer(ctx, sub.ReceiptNo)
		if err != nil {
			r.portalProblem(ctx, err)
			if errors.Is(err, portal.ErrLoginRequired) || errors.Is(err, portal.ErrChallenge) {
				return
			}
			problem = true
			continue
		}
		if ans == nil || strings.TrimSpace(ans.Body) == "" {
			continue
		}
		if _, err := r.d.Store.CreateAnswerFrom(ctx, sub.ID, ans.Body, ans.Dept, "portal"); err != nil {
			log.Printf("답변을 저장하지 못했습니다: %v", err)
			continue
		}
		_ = r.d.Store.AddEvent(ctx, sub.CaseID, "answer_added", "사이트에서 가져옴")
	}
	if !problem {
		r.portalOK(ctx)
	}
}

const portalKey = "portal_attention"

// portalProblem 은 사이트 문제를 한 번만 보고한다(같은 문제를 점검마다 반복하지 않는다).
func (r *Runner) portalProblem(ctx context.Context, err error) {
	if last, _ := r.d.Store.GetSetting(ctx, portalKey); last == err.Error() {
		return
	}
	_ = r.d.Store.SetSetting(ctx, portalKey, err.Error())
	r.send(ctx, report.Report{
		Kind: report.KindAttention, Title: "사이트 점검을 하지 못했습니다",
		Summary: err.Error(),
		Body:    err.Error() + "\n\n해결되면 다음 점검 때 자동으로 이어서 처리합니다.",
	})
}

func (r *Runner) portalOK(ctx context.Context) {
	if last, _ := r.d.Store.GetSetting(ctx, portalKey); last != "" {
		_ = r.d.Store.SetSetting(ctx, portalKey, "")
	}
}

// ---------------------------------------------------------------- 보고

// send 는 모든 보고 채널로 보낸다. 외부 채널이 실패해도 보고함에는 남고, 실패는 활동 기록에 적는다.
func (r *Runner) send(ctx context.Context, rep report.Report) {
	rep.At = r.d.Now()
	if err := r.d.Reporter().Send(ctx, rep); err != nil {
		log.Printf("보고 전송 실패: %v", err)
		if rep.CaseID > 0 {
			_ = r.d.Store.AddEvent(ctx, rep.CaseID, "report_failed", err.Error())
		}
	}
}

// attend 는 사안에 주의 표시를 남기고 보고한다. 같은 내용이 이미 표시되어 있으면 보고를 반복하지 않는다.
func (r *Runner) attend(ctx context.Context, c store.Case, text string, rep report.Report) {
	if cur, err := r.d.Store.GetCase(ctx, c.ID); err == nil && cur.Attention == text {
		return
	}
	_ = r.d.Store.SetAttention(ctx, c.ID, text)
	rep.CaseID, rep.CaseTitle = c.ID, c.Title
	r.send(ctx, rep)
}

func (r *Runner) maybeDaily(ctx context.Context, cfg config.Config) {
	hour := cfg.Auto.DailySummaryHour
	now := r.d.Now().Local()
	if hour < 0 || now.Hour() < hour {
		return
	}
	today := now.Format("2006-01-02")
	if last, _ := r.d.Store.GetSetting(ctx, "daily_last"); last == today {
		return
	}
	_ = r.d.Store.SetSetting(ctx, "daily_last", today)

	reps, err := r.d.Store.ReportsSince(ctx, r.d.Now().Add(-24*time.Hour))
	if err != nil {
		return
	}
	counts := map[string]int{}
	var lines []string
	for _, x := range reps {
		if x.Kind == report.KindDaily || x.Kind == report.KindTest {
			continue
		}
		counts[x.Kind]++
		who := "전체"
		if x.CaseID > 0 {
			who = fmt.Sprintf("사안 #%d", x.CaseID)
		}
		lines = append(lines, fmt.Sprintf("- %s %s: %s", x.CreatedAt.Local().Format("15:04"), who, x.Title))
	}
	if len(lines) == 0 {
		return
	}
	summary := fmt.Sprintf("지난 24시간: 재민원 제출 %d건 · 재민원 초안 %d건 · 종결 %d건 · 확인 필요 %d건",
		counts[report.KindFollowUpSubmitted], counts[report.KindFollowUpDrafted], counts[report.KindClosed],
		counts[report.KindHold]+counts[report.KindAttention])
	r.send(ctx, report.Report{
		Kind: report.KindDaily, Title: "하루 요약", Summary: summary, Body: strings.Join(lines, "\n"),
	})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func parseAsks(js string) []string {
	var asks []string
	_ = json.Unmarshal([]byte(js), &asks)
	return asks
}
