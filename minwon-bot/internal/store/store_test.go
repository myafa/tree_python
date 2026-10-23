package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCaseSubmissionAnswerEvaluationFlow(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	cid, err := s.CreateCase(ctx, Case{Title: "휴가 불승인", Agency: "국방부", Facts: "사실", Desired: "시정"})
	if err != nil {
		t.Fatal(err)
	}
	sub1, err := s.CreateSubmission(ctx, cid, "initial", `{"a":1}`, "제목1", "본문1", `["질의1"]`)
	if err != nil {
		t.Fatal(err)
	}
	if sub1.Round != 1 || sub1.Status != "draft" {
		t.Fatalf("1회차 초안이어야 함: %+v", sub1)
	}
	if err := s.SaveSubmissionText(ctx, sub1.ID, "제목1 수정", "본문1 수정", `["질의1","질의2"]`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSubmitted(ctx, sub1.ID, "2AA-2610-0001"); err != nil {
		t.Fatal(err)
	}
	// 제출 후에는 본문을 고칠 수 없다.
	if err := s.SaveSubmissionText(ctx, sub1.ID, "x", "x", "[]"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("제출 후 수정이 막혀야 함: %v", err)
	}
	ans, err := s.CreateAnswer(ctx, sub1.ID, "답변 본문", "인사복지실")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSubmission(ctx, sub1.ID)
	if got.Status != "answered" || got.ReceiptNo != "2AA-2610-0001" || got.Title != "제목1 수정" {
		t.Fatalf("제출 상태 불일치: %+v", got)
	}
	if _, err := s.LatestEvaluation(ctx, ans.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("평가 전에는 없어야 함: %v", err)
	}
	if _, err := s.SaveEvaluation(ctx, ans.ID, `{"x":1}`, 40, "follow_up"); err != nil {
		t.Fatal(err)
	}
	ev, err := s.LatestEvaluation(ctx, ans.ID)
	if err != nil || ev.Score != 40 || ev.Recommendation != "follow_up" {
		t.Fatalf("평가 불일치: %+v %v", ev, err)
	}
	sub2, err := s.CreateSubmission(ctx, cid, "followup", "", "재민원", "본문2", "[]")
	if err != nil || sub2.Round != 2 {
		t.Fatalf("2회차여야 함: %+v %v", sub2, err)
	}
}

func TestSubmitLimitsQueries(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	base := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return base })
	cid, _ := s.CreateCase(ctx, Case{Title: "t"})
	for i := 0; i < 2; i++ {
		sub, _ := s.CreateSubmission(ctx, cid, "initial", "", "t", "b", "[]")
		s.SetClock(func() time.Time { return base.Add(time.Duration(i) * time.Hour) })
		if err := s.MarkSubmitted(ctx, sub.ID, "N"); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.CountSubmittedSince(ctx, base.Add(-time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("2건이어야 함: %d %v", n, err)
	}
	n, _ = s.CountSubmittedSince(ctx, base.Add(30*time.Minute))
	if n != 1 {
		t.Fatalf("기준 이후 1건이어야 함: %d", n)
	}
	last, _ := s.LastSubmittedAt(ctx)
	if !last.Equal(base.Add(time.Hour)) {
		t.Fatalf("마지막 제출 시각 불일치: %v", last)
	}
}

func TestSettingsStyleAndClose(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, err := s.GetSetting(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal("없으면 ErrNotFound")
	}
	s.SetSetting(ctx, "k", "v1")
	s.SetSetting(ctx, "k", "v2")
	if v, _ := s.GetSetting(ctx, "k"); v != "v2" {
		t.Fatalf("덮어쓰기 실패: %q", v)
	}
	s.AddStyleSample(ctx, "sample", "글1", "")
	s.AddStyleSample(ctx, "edit", "최종", "초안")
	all, _ := s.ListStyleSamples(ctx, "", 10)
	edits, _ := s.ListStyleSamples(ctx, "edit", 10)
	if len(all) != 2 || len(edits) != 1 || edits[0].Original != "초안" {
		t.Fatalf("문체 글 목록 불일치: %d %d", len(all), len(edits))
	}
	cid, _ := s.CreateCase(ctx, Case{Title: "t"})
	if err := s.CloseCase(ctx, cid, "충분히 답변됨"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetCase(ctx, cid)
	if c.Status != "closed" || c.ClosedReason != "충분히 답변됨" {
		t.Fatalf("종결 불일치: %+v", c)
	}
	if err := s.CloseCase(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("없는 사안 종결은 ErrNotFound")
	}
}

func TestAppendCaseFacts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	cid, _ := s.CreateCase(ctx, Case{Title: "t"})
	if err := s.AppendCaseFacts(ctx, cid, "첫 줄"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCaseFacts(ctx, cid, "둘째 줄"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetCase(ctx, cid)
	if c.Facts != "첫 줄\n둘째 줄" {
		t.Fatalf("덧붙이기 불일치: %q", c.Facts)
	}
	if err := s.AppendCaseFacts(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("없는 사안은 ErrNotFound")
	}
}
