package auto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/llm"
	"minwon-bot/internal/minwon"
	"minwon-bot/internal/portal"
	"minwon-bot/internal/report"
	"minwon-bot/internal/store"
)

type action string

const (
	actClose    action = "close"
	actFollowUp action = "followup"
	actNotify   action = "notify"
)

// 종결 사유의 종류. 외부 채널의 요약에는 이 종류만 쓰고 AI 가 쓴 문장은 쓰지 않는다.
const (
	kindSufficient = "충분한 답변"
	kindOtherRoute = "다른 경로 권고"
	kindLimit      = "재민원 한도 도달"
	kindHoldPolicy = "판단 보류 후 설정에 따라 종결"
)

type decision struct {
	act    action
	kind   string // 종결 사유의 종류
	reason string // 사유 전문(보고함에만 기록)
	routes []string
}

// decide 는 평가 결과와 한도로 다음 행동을 정한다. 순수 함수라 AI 나 DB 를 부르지 않는다.
func decide(cfg config.Config, nextRound int, ev minwon.EvalResult) decision {
	switch ev.Recommendation {
	case minwon.RecClose:
		return decision{act: actClose, kind: kindSufficient,
			reason: fmt.Sprintf("답변이 충분합니다(%d점). %s", ev.Score, ev.RecommendationReason)}
	case minwon.RecOtherRoute:
		return decision{act: actClose, kind: kindOtherRoute, routes: ev.OtherRoutes,
			reason: "이 기관에 반복해도 소용이 적어 다른 경로를 권고합니다. " + ev.RecommendationReason}
	case minwon.RecFollowUp:
		if nextRound >= cfg.Limits.RepeatRound {
			return decision{act: actClose, kind: kindLimit, routes: ev.OtherRoutes,
				reason: fmt.Sprintf("재민원이 필요해 보이지만 %d회차부터는 반복민원으로 종결될 수 있어 자동으로 내지 않습니다. 같은 기관에 다시 내기보다 다른 경로를 검토하세요. %s",
					cfg.Limits.RepeatRound, ev.RecommendationReason)}
		}
		return decision{act: actFollowUp}
	}
	// hold
	if cfg.Auto.HoldPolicy == config.HoldClose {
		return decision{act: actClose, kind: kindHoldPolicy, routes: ev.OtherRoutes,
			reason: "AI 가 판단을 보류했지만 설정에 따라 종결합니다. " + ev.RecommendationReason}
	}
	return decision{act: actNotify, reason: "AI 가 판단을 보류했습니다. " + ev.RecommendationReason}
}

// ProcessAnswer 는 답변 한 건을 평가하고 결정에 따라 재민원 또는 종결을 실행한 뒤 보고한다.
// 실패하면 답변을 미처리로 남겨 다음 점검에서 다시 시도한다(횟수 제한 있음).
func (r *Runner) ProcessAnswer(ctx context.Context, ans store.Answer) {
	r.d.Lock.Lock()
	defer r.d.Lock.Unlock()

	cur, err := r.d.Store.GetAnswer(ctx, ans.ID)
	if err != nil || !cur.ProcessedAt.IsZero() {
		return // 이미 처리했거나 사라짐
	}
	sub, err := r.d.Store.GetSubmission(ctx, cur.SubmissionID)
	if err != nil {
		return
	}
	c, err := r.d.Store.GetCase(ctx, sub.CaseID)
	if err != nil || c.Status != "open" || c.AutoOff {
		return
	}
	cfg := r.d.Config()

	subs, err := r.d.Store.ListSubmissions(ctx, c.ID)
	if err != nil {
		return
	}
	// 이미 다음 회차를 제출했다면 이 답변은 지난 회차의 것이다. 뒤늦게 처리하면 진행 중인 회차를 무시하고
	// 사안을 종결하거나 중복 재민원을 만들 수 있으므로, 아무 조치 없이 처리 표시만 한다.
	if sub.Round < maxRound(subs) {
		_ = r.d.Store.MarkAnswerProcessed(ctx, cur.ID)
		_ = r.d.Store.AddEvent(ctx, c.ID, "answer_added", fmt.Sprintf("(%d회차 답변은 이미 다음 회차가 진행 중이라 자동 처리하지 않음)", sub.Round))
		return
	}

	ev, err := r.evaluate(ctx, c, sub, cur)
	if err != nil {
		r.failed(ctx, c, cur, "답변 평가", err)
		return
	}
	dec := decide(cfg, maxRound(subs)+1, ev)
	switch dec.act {
	case actClose:
		r.closeCase(ctx, c, sub, cur, ev, dec)
	case actNotify:
		r.notify(ctx, c, cur, report.KindHold, "판단을 보류했습니다 — 확인이 필요합니다",
			"AI 판단 보류", dec.reason, ev)
	case actFollowUp:
		r.followUp(ctx, cfg, c, sub, cur, ev, subs)
	}
}

func (r *Runner) evaluate(ctx context.Context, c store.Case, sub store.Submission, ans store.Answer) (minwon.EvalResult, error) {
	if e, err := r.d.Store.LatestEvaluation(ctx, ans.ID); err == nil {
		var res minwon.EvalResult
		if json.Unmarshal([]byte(e.ResultJSON), &res) == nil {
			return res, nil
		}
	}
	res, err := r.d.Service().Evaluate(ctx, minwon.EvalInput{
		Title: sub.Title, Body: sub.Body, Asks: parseAsks(sub.AsksJSON),
		Answer: ans.Body, Dept: ans.Dept, Round: sub.Round,
	})
	if err != nil {
		return res, err
	}
	if _, err := r.d.Store.SaveEvaluation(ctx, ans.ID, mustJSON(res), res.Score, res.Recommendation); err != nil {
		return res, err
	}
	_ = r.d.Store.AddEvent(ctx, c.ID, "evaluated", "(자동)")
	return res, nil
}

// failed 는 AI 호출 실패를 다룬다. 거절은 다시 해도 같으므로 바로 보류하고, 그 밖의 실패는 몇 번 더 시도한다.
func (r *Runner) failed(ctx context.Context, c store.Case, ans store.Answer, what string, err error) {
	if errors.Is(err, llm.ErrRefusal) {
		r.notify(ctx, c, ans, report.KindHold, "AI 가 응답하지 않아 보류했습니다 — 확인이 필요합니다",
			"AI 안전장치로 응답 거절", what+"에서 AI 가 응답하지 않았습니다. 직접 확인하세요.\n"+err.Error(), minwon.EvalResult{})
		return
	}
	r.mu.Lock()
	r.fails[ans.ID]++
	n := r.fails[ans.ID]
	r.mu.Unlock()
	log.Printf("자동 처리 실패(%s, 답변 %d, %d회): %v", what, ans.ID, n, err)
	if n >= maxFails {
		r.notify(ctx, c, ans, report.KindAttention, "자동 처리를 포기했습니다 — 확인이 필요합니다",
			fmt.Sprintf("%s을(를) %d번 시도했지만 실패", what, maxFails),
			fmt.Sprintf("%s을(를) %d번 시도했지만 실패해 자동 처리를 포기했습니다. 직접 평가하거나 설정의 AI 연결을 확인하세요.\n마지막 오류: %v", what, maxFails, err),
			minwon.EvalResult{})
		return
	}
	text := fmt.Sprintf("%s 실패, 다음 점검에서 다시 시도합니다", what)
	r.attend(ctx, c, text, report.Report{
		Kind: report.KindAttention, Title: "자동 처리가 잠시 실패했습니다", Summary: text,
		Body: fmt.Sprintf("%s 중 오류: %v\n다음 점검에서 다시 시도하며, %d번 실패하면 멈추고 알려 드립니다.", what, err, maxFails),
	})
}

// notify 는 자동으로 결정하지 않고 사람에게 알린다. 사안은 열어 둔다.
func (r *Runner) notify(ctx context.Context, c store.Case, ans store.Answer, kind, title, summary, body string, ev minwon.EvalResult) {
	if err := r.d.Store.MarkAnswerProcessed(ctx, ans.ID); err != nil {
		log.Printf("처리 표시 실패: %v", err)
	}
	rep := report.Report{Kind: kind, Title: title, Summary: summary, Body: body + evalBlock(ev)}
	_ = r.d.Store.SetAttention(ctx, c.ID, summary)
	rep.CaseID, rep.CaseTitle = c.ID, c.Title
	r.send(ctx, rep)
}

func (r *Runner) closeCase(ctx context.Context, c store.Case, sub store.Submission, ans store.Answer, ev minwon.EvalResult, dec decision) {
	if err := r.d.Store.CloseCase(ctx, c.ID, "(자동) "+dec.reason); err != nil {
		log.Printf("종결 실패: %v", err)
		return
	}
	_ = r.d.Store.AddEvent(ctx, c.ID, "closed", "(자동) "+dec.kind)
	_ = r.d.Store.MarkAnswerProcessed(ctx, ans.ID)

	var b strings.Builder
	b.WriteString("종결 사유: " + dec.reason + "\n")
	if len(dec.routes) > 0 {
		b.WriteString("\n검토할 다른 경로:\n")
		for _, x := range dec.routes {
			b.WriteString("- " + x + "\n")
		}
	}
	b.WriteString(evalBlock(ev))
	b.WriteString("\n되돌리려면 앱의 사안 화면에서 '다시 열기'를 누르세요.")
	r.send(ctx, report.Report{
		Kind: report.KindClosed, CaseID: c.ID, CaseTitle: c.Title, Title: "사안을 종결했습니다",
		Summary: fmt.Sprintf("%d회차 답변 평가 %d점 · 종결 유형: %s", sub.Round, ev.Score, dec.kind),
		Body:    b.String(),
	})
}

// evalBlock 은 보고함에 남길 평가 요약이다.
func evalBlock(ev minwon.EvalResult) string {
	if len(ev.Items) == 0 {
		return ""
	}
	labels := map[string]string{
		minwon.VerdictSufficient: "충분", minwon.VerdictPartial: "일부만", minwon.VerdictEvasive: "회피",
		minwon.VerdictUnanswered: "무응답", minwon.VerdictUndetermined: "판정 불가",
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n답변 평가 %d점: %s\n", ev.Score, ev.Summary)
	for _, it := range ev.Items {
		fmt.Fprintf(&b, "- [%s] %s\n", labels[it.Verdict], it.Ask)
	}
	return b.String()
}

// maxRound 는 이미 제출한 회차의 최대값이다. 아직 제출하지 않은 초안은 세지 않는다.
// (미뤄 둔 재민원 초안이 다음 점검에서 회차를 하나 더 올려 사안이 종결되는 일을 막는다.)
func maxRound(subs []store.Submission) int {
	m := 0
	for _, s := range subs {
		if s.Status != "draft" {
			m = max(m, s.Round)
		}
	}
	return m
}

// ---------------------------------------------------------------- 재민원

var temporary = map[string]bool{"daily_limit": true, "min_gap": true}

// autoBlockers 는 자동 제출을 막는 점검 결과다. 화면에서는 경고인 것(이전 글과 비슷함, 본문 속 개인정보)도
// 사람이 보지 않는 자동 제출에서는 막는다.
func autoBlockers(fs []minwon.Finding) []minwon.Finding {
	var out []minwon.Finding
	for _, f := range fs {
		if f.Severity == minwon.SevBlock || f.Code == "similar_to_previous" || f.Code == "pii" {
			out = append(out, f)
		}
	}
	return out
}

func allTemporary(fs []minwon.Finding) bool {
	for _, f := range fs {
		if !temporary[f.Code] {
			return false
		}
	}
	return len(fs) > 0
}

func (r *Runner) guardInput(ctx context.Context, cfg config.Config, c store.Case, d store.Submission, subs []store.Submission) minwon.GuardInput {
	var prev []string
	for _, o := range subs {
		if o.ID != d.ID && o.Round < d.Round {
			prev = append(prev, o.Body)
		}
	}
	now := r.d.Now()
	n, _ := r.d.Store.CountSubmittedSince(ctx, now.Add(-24*time.Hour))
	last, _ := r.d.Store.LastSubmittedAt(ctx)
	return minwon.GuardInput{
		Round: d.Round, Title: d.Title, Body: d.Body, PrevBodies: prev,
		SubmittedLast24h: n, LastSubmittedAt: last, Now: now, Limits: cfg.Limits,
	}
}

// 사용자가 초안을 고쳤는지: 저장된 제목·본문·질의가 AI 가 처음 쓴 것과 같은지 본다.
func untouched(d store.Submission) bool {
	var orig minwon.DraftResult
	if json.Unmarshal([]byte(d.DraftJSON), &orig) != nil {
		return false
	}
	return strings.TrimSpace(orig.Title) == strings.TrimSpace(d.Title) &&
		strings.TrimSpace(orig.Body) == strings.TrimSpace(d.Body) &&
		mustJSON(orig.Asks) == mustJSON(parseAsks(d.AsksJSON))
}

func autoDraftKey(id int64) string { return fmt.Sprintf("auto_draft:%d", id) }

func (r *Runner) isAutoDraft(ctx context.Context, id int64) bool {
	v, err := r.d.Store.GetSetting(ctx, autoDraftKey(id))
	return err == nil && v == "1"
}

func findFollowUpDraft(subs []store.Submission, round int) *store.Submission {
	for i := range subs {
		if subs[i].Status == "draft" && subs[i].Kind == "followup" && subs[i].Round == round {
			return &subs[i]
		}
	}
	return nil
}

func (r *Runner) followUp(ctx context.Context, cfg config.Config, c store.Case, sub store.Submission, ans store.Answer, ev minwon.EvalResult, subs []store.Submission) {
	next := maxRound(subs) + 1
	draft := findFollowUpDraft(subs, next)
	// 자동 처리가 만든 초안만 다시 쓴다. 사용자가 직접 만든 초안을 대신 제출하지 않기 위해서다.
	if draft != nil && !r.isAutoDraft(ctx, draft.ID) {
		r.notify(ctx, c, ans, report.KindHold, "작성 중인 재민원 초안이 있어 자동 처리를 하지 않았습니다",
			"직접 만든 재민원 초안이 이미 있음",
			"이 사안에는 직접 만든 재민원 초안이 이미 있어 자동으로 제출하지 않았습니다. 사안 화면에서 확인하세요.", ev)
		return
	}

	if draft == nil {
		// 사이트로 제출할 거라면 일시적 한도를 먼저 보고 AI 호출을 아낀다.
		if r.d.Portal != nil {
			probe := r.guardInput(ctx, cfg, c, store.Submission{Round: 1, Title: "x", Body: "x"}, nil)
			if fs := autoBlockers(minwon.Check(probe)); allTemporary(fs) {
				r.deferFollowUp(ctx, c, fs)
				return
			}
		}
		res, err := r.d.Service().FollowUp(ctx, minwon.FollowUpInput{
			Agency: c.Agency, Round: next, PrevReceipt: sub.ReceiptNo,
			PrevSubmitAt: sub.SubmittedAt.Local().Format("2006년 1월 2일"),
			PrevTitle:    sub.Title, PrevBody: sub.Body, PrevAsks: parseAsks(sub.AsksJSON),
			Answer: ans.Body, Dept: ans.Dept, Eval: ev, Style: r.d.Style(ctx),
		})
		if errors.Is(err, minwon.ErrNothingToFollowUp) {
			r.notify(ctx, c, ans, report.KindHold, "재민원할 항목이 없어 보류했습니다 — 확인이 필요합니다",
				"재민원 대상 항목 없음", "평가는 재민원을 권고했지만 미흡한 항목이 없습니다. 직접 확인하세요.", ev)
			return
		}
		if err != nil {
			r.failed(ctx, c, ans, "재민원 초안 작성", err)
			return
		}
		d, err := r.d.Store.CreateSubmission(ctx, c.ID, "followup", mustJSON(res), res.Title, res.Body, mustJSON(res.Asks))
		if err != nil {
			log.Printf("재민원 초안 저장 실패: %v", err)
			return
		}
		_ = r.d.Store.SetSetting(ctx, autoDraftKey(d.ID), "1")
		_ = r.d.Store.AddEvent(ctx, c.ID, "followup_created", "(자동)")
		draft = &d
		subs = append(subs, d)
	} else if !untouched(*draft) {
		r.notify(ctx, c, ans, report.KindHold, "재민원 초안이 수정되어 자동 제출하지 않았습니다",
			"사용자가 고친 초안은 자동 제출하지 않음",
			"자동으로 만든 재민원 초안을 직접 고치셨으므로 자동 제출하지 않았습니다. 사안 화면에서 확인하고 직접 제출하세요.", ev)
		return
	}

	// 제출 전 점검. 사이트로 제출하지 않는 경우(연동 없음)에는 제출 한도가 의미 없으므로 뺀다.
	fs := autoBlockers(minwon.Check(r.guardInput(ctx, cfg, c, *draft, subs)))
	if r.d.Portal == nil {
		var keep []minwon.Finding
		for _, f := range fs {
			if !temporary[f.Code] {
				keep = append(keep, f)
			}
		}
		fs = keep
	}
	if len(fs) > 0 {
		if allTemporary(fs) {
			r.deferFollowUp(ctx, c, fs)
			return
		}
		var msgs []string
		for _, f := range fs {
			msgs = append(msgs, "- "+f.Message)
		}
		r.notify(ctx, c, ans, report.KindHold, "재민원 초안을 만들었지만 자동 제출하지 않았습니다 — 확인이 필요합니다",
			"제출 전 점검에서 걸림",
			"자동 제출을 막은 이유:\n"+strings.Join(msgs, "\n")+"\n\n초안은 사안 화면에 남겨 두었습니다. 내용을 확인하고 직접 제출하세요.\n\n[재민원 초안]\n"+draft.Title+"\n"+draft.Body, ev)
		return
	}

	if r.d.Portal == nil {
		_ = r.d.Store.MarkAnswerProcessed(ctx, ans.ID)
		_ = r.d.Store.SetAttention(ctx, c.ID, "재민원 초안이 준비되었습니다. 직접 제출하세요")
		r.send(ctx, report.Report{
			Kind: report.KindFollowUpDrafted, CaseID: c.ID, CaseTitle: c.Title,
			Title:   "재민원 초안을 준비했습니다 — 직접 제출해 주세요",
			Summary: fmt.Sprintf("%d회차 재민원 초안 · 사이트 자동 제출은 설정되지 않음", draft.Round),
			Body:    "[제목] " + draft.Title + "\n\n" + draft.Body + "\n\n앱의 사안 화면에서 초안을 확인하고 국민신문고에 직접 제출한 뒤 접수번호를 기록하세요." + evalBlock(ev),
		})
		return
	}
	r.submit(ctx, c, ans, *draft, ev)
}

func (r *Runner) deferFollowUp(ctx context.Context, c store.Case, fs []minwon.Finding) {
	var msgs []string
	for _, f := range fs {
		msgs = append(msgs, f.Message)
	}
	text := "재민원 제출을 미뤘습니다. 한도가 풀리면 자동으로 이어서 처리합니다"
	r.attend(ctx, c, text, report.Report{
		Kind: report.KindAttention, Title: "재민원 제출을 미뤘습니다", Summary: "제출 한도(하루 건수 또는 최소 간격) 때문에 나중에 다시 시도",
		Body: strings.Join(msgs, "\n") + "\n\n재민원 초안은 만들어 두었고, 한도가 풀리면 다음 점검에서 자동으로 제출합니다. 직접 제출하려면 먼저 이 사안의 자동 처리를 끄세요.",
	})
}

func (r *Runner) submit(ctx context.Context, c store.Case, ans store.Answer, draft store.Submission, ev minwon.EvalResult) {
	if err := r.d.Portal.EnsureLogin(ctx); err != nil {
		r.portalProblem(ctx, err)
		return // 답변을 미처리로 남겨 로그인 후 다음 점검에서 이어서 처리
	}
	private := true
	var d minwon.DraftResult
	if json.Unmarshal([]byte(draft.DraftJSON), &d) == nil && d.Visibility == "public" {
		private = false
	}
	rec, err := r.d.Portal.Submit(ctx, portal.Form{Title: draft.Title, Body: draft.Body, Agency: c.Agency, Private: private})
	switch {
	case err == nil:
	case errors.Is(err, portal.ErrLoginRequired):
		r.portalProblem(ctx, err)
		return
	default:
		// 추가 인증, 예상 밖 화면 등. 제출이 되었을 수도 있으므로 다시 시도하지 않고 사람에게 넘긴다.
		_ = r.d.Store.SetCaseAutoOff(ctx, c.ID, true)
		_ = r.d.Store.MarkAnswerProcessed(ctx, ans.ID)
		text := "자동 제출 중 멈췄습니다 — 사이트에서 제출 여부를 확인하세요"
		_ = r.d.Store.SetAttention(ctx, c.ID, text)
		r.send(ctx, report.Report{
			Kind: report.KindAttention, CaseID: c.ID, CaseTitle: c.Title, Title: "자동 제출 중 멈췄습니다 — 확인이 필요합니다",
			Summary: "사이트 제출이 완료되지 않았거나 확인되지 않음. 이 사안의 자동 처리를 껐습니다",
			Body: "사유: " + err.Error() + "\n\n제출이 사이트에 이미 접수되었을 수도 있으니 국민신문고의 '나의 민원'에서 먼저 확인하세요. " +
				"이 사안의 자동 처리는 꺼 두었습니다. 처리를 마친 뒤 앱의 사안 화면에서 다시 켜세요.\n\n[재민원 초안]\n" + draft.Title + "\n" + draft.Body,
		})
		return
	}
	if err := r.d.Store.MarkSubmitted(ctx, draft.ID, rec.No); err != nil {
		log.Printf("접수번호 기록 실패(사이트에는 제출됨 %s): %v", rec.No, err)
	}
	_ = r.d.Store.MarkAnswerProcessed(ctx, ans.ID)
	_ = r.d.Store.SetAttention(ctx, c.ID, "")
	_ = r.d.Store.AddEvent(ctx, c.ID, "submitted", "(자동) 접수번호 "+rec.No)
	r.send(ctx, report.Report{
		Kind: report.KindFollowUpSubmitted, CaseID: c.ID, CaseTitle: c.Title, Title: "재민원을 제출했습니다",
		Summary: fmt.Sprintf("%d회차 재민원 · 접수번호 %s", draft.Round, rec.No),
		Body: "[제목] " + draft.Title + "\n\n" + draft.Body +
			"\n\n이미 제출한 재민원은 사이트에서 직접 취하해야 합니다. 이 사안의 자동 처리를 멈추려면 앱의 사안 화면에서 '자동 처리 끄기'를 누르세요." + evalBlock(ev),
	})
}
