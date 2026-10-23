// Package report 는 자동 처리 결과를 사용자에게 알리는 사후보고 채널을 담는다.
// 웹 화면의 보고함은 항상 켜져 있고, 텔레그램과 이메일은 설정했을 때만 쓴다.
package report

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/store"
)

const (
	KindFollowUpSubmitted = "followup_submitted" // 재민원을 사이트에 제출함
	KindFollowUpDrafted   = "followup_drafted"   // 재민원 초안만 준비함(직접 제출 필요)
	KindClosed            = "closed"             // 사안을 종결함
	KindHold              = "hold"               // 판단을 보류함(확인 필요)
	KindAttention         = "attention"          // 로그인 만료, 추가 인증 등 사람이 해야 할 일
	KindDaily             = "daily"              // 하루 요약
	KindTest              = "test"               // 연결 시험
)

// Report 는 보고 한 건이다. Title 과 Summary 에는 민원 내용이나 개인정보를 넣지 않는다.
// 외부 채널은 기본적으로 이 둘만 보낸다.
type Report struct {
	Kind      string
	CaseID    int64
	CaseTitle string
	Title     string // 예: "재민원을 제출했습니다"
	Summary   string // 예: "2회차 · 접수번호 2AA-2610-0002"
	Body      string // 전체 내용(민원 본문, 답변 요지, 평가). 보고함에는 항상 저장한다.
	At        time.Time
}

type Reporter interface {
	Name() string
	Send(ctx context.Context, r Report) error
}

const footer = "(앱의 보고함에서 자세한 내용을 확인할 수 있습니다)"

// External 은 텔레그램·이메일로 보낼 글이다. full 이 아니면 내용 없는 요약만 담는다.
func (r Report) External(full bool) string {
	var b strings.Builder
	b.WriteString("[민원 도우미] ")
	if full && r.CaseTitle != "" {
		fmt.Fprintf(&b, "사안 #%d %s: ", r.CaseID, r.CaseTitle)
	} else if r.CaseID > 0 {
		fmt.Fprintf(&b, "사안 #%d: ", r.CaseID)
	}
	b.WriteString(r.Title)
	if r.Summary != "" {
		b.WriteString("\n" + r.Summary)
	}
	if full && strings.TrimSpace(r.Body) != "" {
		b.WriteString("\n\n" + r.Body)
	} else {
		b.WriteString("\n" + footer)
	}
	return b.String()
}

// InboxBody 는 보고함에 저장할 본문이다.
func (r Report) InboxBody() string {
	switch {
	case r.Summary != "" && r.Body != "":
		return r.Summary + "\n\n" + r.Body
	case r.Summary != "":
		return r.Summary
	}
	return r.Body
}

// Inbox 는 보고를 DB 의 보고함에 쌓는다.
type Inbox struct{ Store *store.Store }

func (Inbox) Name() string { return "보고함" }

func (i Inbox) Send(ctx context.Context, r Report) error {
	_, err := i.Store.AddReport(ctx, store.Report{CaseID: r.CaseID, Kind: r.Kind, Title: r.Title, Body: r.InboxBody()})
	return err
}

// Multi 는 여러 채널에 모두 보낸다. 일부가 실패해도 나머지는 계속 보내고, 실패는 모아서 돌려준다.
type Multi []Reporter

func (m Multi) Name() string {
	var names []string
	for _, r := range m {
		names = append(names, r.Name())
	}
	return strings.Join(names, ", ")
}

func (m Multi) Send(ctx context.Context, r Report) error {
	var errs []error
	for _, rep := range m {
		if err := rep.Send(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rep.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// TelegramFrom, EmailFrom 은 설정에서 채널을 만든다. 설정이 없으면 nil.
func TelegramFrom(cfg config.Config) *Telegram {
	if !cfg.TelegramEnabled() {
		return nil
	}
	return &Telegram{Token: cfg.TelegramToken(), ChatID: cfg.Report.Telegram.ChatID, Full: cfg.Report.Detail == "full"}
}

func EmailFrom(cfg config.Config) *Email {
	if !cfg.EmailEnabled() {
		return nil
	}
	e := cfg.Report.Email
	return &Email{
		Host: e.Host, Port: e.Port, Username: e.Username, Password: cfg.EmailPassword(),
		From: e.From, To: e.To, Mode: e.TLS, Full: cfg.Report.Detail == "full",
	}
}

// Build 는 설정에 맞는 보고 채널 묶음을 만든다. 보고함이 항상 첫 번째다.
func Build(cfg config.Config, st *store.Store) Reporter {
	m := Multi{Inbox{Store: st}}
	if t := TelegramFrom(cfg); t != nil {
		m = append(m, t)
	}
	if e := EmailFrom(cfg); e != nil {
		m = append(m, e)
	}
	return m
}
