package store

import (
	"context"
	"database/sql"
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

// M1 시절(자동 처리 컬럼이 없던) DB 를 열어도 데이터가 그대로이고 새 컬럼이 생겨야 한다.
func TestMigrateFromM1Schema(t *testing.T) {
	path := t.TempDir() + "/old.db"
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`
CREATE TABLE cases (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, agency TEXT NOT NULL DEFAULT '',
  facts TEXT NOT NULL DEFAULT '', desired TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open',
  closed_reason TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE submissions (id INTEGER PRIMARY KEY AUTOINCREMENT, case_id INTEGER NOT NULL, round INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'initial', draft_json TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '', asks_json TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'draft',
  receipt_no TEXT NOT NULL DEFAULT '', submitted_at TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE answers (id INTEGER PRIMARY KEY AUTOINCREMENT, submission_id INTEGER NOT NULL, body TEXT NOT NULL,
  dept TEXT NOT NULL DEFAULT '', received_at TEXT NOT NULL);
INSERT INTO cases(title,facts,created_at) VALUES('옛 사안','옛 사실','2026-10-01T00:00:00Z');
INSERT INTO submissions(case_id,round,status,receipt_no,created_at) VALUES(1,1,'answered','N1','2026-10-01T00:00:00Z');
INSERT INTO answers(submission_id,body,received_at) VALUES(1,'옛 답변','2026-10-02T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("옛 DB 를 열지 못함: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	c, err := s.GetCase(ctx, 1)
	if err != nil || c.Title != "옛 사안" || c.AutoOff || c.Attention != "" {
		t.Fatalf("옛 사안이 유지되고 새 컬럼은 기본값이어야 함: %+v %v", c, err)
	}
	a, err := s.GetAnswer(ctx, 1)
	if err != nil || a.Body != "옛 답변" || a.Source != "pasted" || !a.ProcessedAt.IsZero() {
		t.Fatalf("옛 답변이 유지되어야 함: %+v %v", a, err)
	}
	// 두 번 열어도 오류가 없어야 한다(이미 컬럼이 있음).
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("재오픈 실패: %v", err)
	}
	s2.Close()
}

func TestPendingAnswersAndAwaitingSubmissions(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	mk := func(title string) (int64, Submission) {
		cid, _ := s.CreateCase(ctx, Case{Title: title})
		sub, _ := s.CreateSubmission(ctx, cid, "initial", "", "t", "b", `["q"]`)
		return cid, sub
	}
	c1, s1 := mk("진행 중, 답변 대기")
	s.MarkSubmitted(ctx, s1.ID, "R1")
	c2, s2 := mk("진행 중, 답변 있음")
	s.MarkSubmitted(ctx, s2.ID, "R2")
	a2, _ := s.CreateAnswerFrom(ctx, s2.ID, "답변2", "부서", "portal")
	c3, s3 := mk("종결")
	s.MarkSubmitted(ctx, s3.ID, "R3")
	s.CreateAnswer(ctx, s3.ID, "답변3", "")
	s.CloseCase(ctx, c3, "끝")
	c4, s4 := mk("자동 처리 끔")
	s.MarkSubmitted(ctx, s4.ID, "R4")
	s.CreateAnswer(ctx, s4.ID, "답변4", "")
	s.SetCaseAutoOff(ctx, c4, true)
	_, s5 := mk("초안(제출 안 함)")
	_ = s5

	pending, _ := s.ListPendingAnswers(ctx)
	if len(pending) != 1 || pending[0].ID != a2.ID || pending[0].Source != "portal" {
		t.Fatalf("처리 대기 답변은 진행 중·자동 처리 켠 사안의 것 1건: %+v", pending)
	}
	awaiting, _ := s.ListAwaitingSubmissions(ctx)
	if len(awaiting) != 1 || awaiting[0].ID != s1.ID || awaiting[0].CaseID != c1 {
		t.Fatalf("답변 대기 제출은 1건: %+v", awaiting)
	}
	if err := s.MarkAnswerProcessed(ctx, a2.ID); err != nil {
		t.Fatal(err)
	}
	if pending, _ = s.ListPendingAnswers(ctx); len(pending) != 0 {
		t.Fatalf("처리 표시 후에는 없어야 함: %+v", pending)
	}
	got, _ := s.GetAnswer(ctx, a2.ID)
	if got.ProcessedAt.IsZero() {
		t.Fatal("처리 시각이 기록되어야 함")
	}
	_ = c2
}

func TestReopenAttentionAndReports(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	cid, _ := s.CreateCase(ctx, Case{Title: "t"})
	s.SetAttention(ctx, cid, "로그인 필요")
	c, _ := s.GetCase(ctx, cid)
	if c.Attention != "로그인 필요" {
		t.Fatalf("주의 표시: %+v", c)
	}
	s.CloseCase(ctx, cid, "끝")
	c, _ = s.GetCase(ctx, cid)
	if c.Status != "closed" || c.Attention != "" {
		t.Fatalf("종결하면 주의 표시가 지워져야 함: %+v", c)
	}
	if err := s.ReopenCase(ctx, cid); err != nil {
		t.Fatal(err)
	}
	c, _ = s.GetCase(ctx, cid)
	if c.Status != "open" || c.ClosedReason != "" {
		t.Fatalf("다시 열기: %+v", c)
	}

	base := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return base })
	s.AddReport(ctx, Report{CaseID: cid, Kind: "closed", Title: "첫 보고", Body: "본문1"})
	s.SetClock(func() time.Time { return base.Add(2 * time.Hour) })
	s.AddReport(ctx, Report{CaseID: cid, Kind: "followup_submitted", Title: "둘째 보고"})
	if n, _ := s.CountUnreadReports(ctx); n != 2 {
		t.Fatalf("안 읽은 보고 2건: %d", n)
	}
	list, _ := s.ListReports(ctx, 10)
	if len(list) != 2 || list[0].Title != "둘째 보고" || list[1].Body != "본문1" {
		t.Fatalf("최근 순이어야 함: %+v", list)
	}
	since, _ := s.ReportsSince(ctx, base.Add(time.Hour))
	if len(since) != 1 || since[0].Title != "둘째 보고" {
		t.Fatalf("이후 보고 1건: %+v", since)
	}
	s.MarkReportsRead(ctx)
	if n, _ := s.CountUnreadReports(ctx); n != 0 {
		t.Fatalf("모두 읽음 처리: %d", n)
	}
}
