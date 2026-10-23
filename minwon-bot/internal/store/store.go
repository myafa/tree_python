// Package store 는 사안, 제출, 답변, 평가를 SQLite 에 저장한다.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var ErrNotFound = errors.New("찾을 수 없습니다")

const timeLayout = time.RFC3339

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Case struct {
	ID           int64
	Title        string
	Agency       string
	Facts        string
	Desired      string
	Status       string
	ClosedReason string
	CreatedAt    time.Time
	// AutoOff 가 true 면 이 사안은 자동 처리와 주기 점검에서 뺀다.
	AutoOff bool
	// Attention 은 사람이 봐야 하는 이유. 비어 있으면 없음.
	Attention string
}

type Submission struct {
	ID          int64
	CaseID      int64
	Round       int
	Kind        string
	DraftJSON   string
	Title       string
	Body        string
	AsksJSON    string
	Status      string
	ReceiptNo   string
	SubmittedAt time.Time
	CreatedAt   time.Time
}

type Answer struct {
	ID           int64
	SubmissionID int64
	Body         string
	Dept         string
	ReceivedAt   time.Time
	// Source 는 "pasted"(사용자가 붙여넣음) 또는 "portal"(사이트에서 가져옴).
	Source string
	// ProcessedAt 은 자동 처리를 마친 시각. zero 면 아직 처리 전이다.
	ProcessedAt time.Time
}

type Report struct {
	ID        int64
	CaseID    int64
	Kind      string
	Title     string
	Body      string
	Read      bool
	CreatedAt time.Time
}

type Evaluation struct {
	ID             int64
	AnswerID       int64
	ResultJSON     string
	Score          int
	Recommendation string
	CreatedAt      time.Time
}

type Event struct {
	ID     int64
	CaseID int64
	Kind   string
	Detail string
	At     time.Time
}

type StyleSample struct {
	ID        int64
	Kind      string
	Text      string
	Original  string
	CreatedAt time.Time
}

// Open 은 path 의 DB 를 열고 스키마를 만든다. ":memory:" 도 가능하다.
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		// WAL 모드는 -wal, -shm 파일을 따로 만드는데 이 파일들의 권한이 DB 본체와 달라
		// 사안 내용이 다른 사용자에게 읽힐 수 있다. 쓰기 한 개짜리 단일 사용자 도구라 기본 저널로 충분하다.
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	} else {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 는 쓰기 한 개씩이면 충분하고, :memory: 는 연결마다 DB 가 따로 생기므로 1개로 고정한다.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("스키마 생성 실패: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("스키마 이전 실패: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// 예전 버전의 DB 에 없는 컬럼을 채운다. 새 DB 도 같은 경로로 만들어 두 경우가 갈라지지 않게 한다.
var columnMigrations = []struct{ table, column, ddl string }{
	{"cases", "auto_off", "INTEGER NOT NULL DEFAULT 0"},
	{"cases", "attention", "TEXT NOT NULL DEFAULT ''"},
	{"answers", "source", "TEXT NOT NULL DEFAULT 'pasted'"},
	{"answers", "processed_at", "TEXT NOT NULL DEFAULT ''"},
}

func migrate(db *sql.DB) error {
	for _, m := range columnMigrations {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, m.table, m.column).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + m.table + ` ADD COLUMN ` + m.column + ` ` + m.ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// SetClock 은 테스트에서 시간을 고정할 때 쓴다.
func (s *Store) SetClock(f func() time.Time) { s.now = f }

func (s *Store) ts() string { return s.now().UTC().Format(timeLayout) }

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---- cases ----

func (s *Store) CreateCase(ctx context.Context, c Case) (int64, error) {
	r, err := s.db.ExecContext(ctx,
		`INSERT INTO cases(title, agency, facts, desired, created_at) VALUES(?,?,?,?,?)`,
		c.Title, c.Agency, c.Facts, c.Desired, s.ts())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

const caseCols = `id,title,agency,facts,desired,status,closed_reason,created_at,auto_off,attention`

func scanCase(r scanner) (Case, error) {
	var c Case
	var created string
	var off int
	err := r.Scan(&c.ID, &c.Title, &c.Agency, &c.Facts, &c.Desired, &c.Status, &c.ClosedReason, &created, &off, &c.Attention)
	c.CreatedAt = parseTime(created)
	c.AutoOff = off != 0
	return c, err
}

func (s *Store) GetCase(ctx context.Context, id int64) (Case, error) {
	c, err := scanCase(s.db.QueryRowContext(ctx, `SELECT `+caseCols+` FROM cases WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) ListCases(ctx context.Context) ([]Case, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+caseCols+` FROM cases ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Case
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AppendCaseFacts(ctx context.Context, id int64, text string) error {
	r, err := s.db.ExecContext(ctx,
		`UPDATE cases SET facts = CASE WHEN facts = '' THEN ? ELSE facts || char(10) || ? END WHERE id=?`, text, text, id)
	return affected(r, err)
}

func (s *Store) CloseCase(ctx context.Context, id int64, reason string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE cases SET status='closed', closed_reason=?, attention='' WHERE id=?`, reason, id)
	return affected(r, err)
}

// ReopenCase 는 종결한 사안을 다시 연다. 자동 종결을 되돌릴 때 쓴다.
func (s *Store) ReopenCase(ctx context.Context, id int64) error {
	r, err := s.db.ExecContext(ctx, `UPDATE cases SET status='open', closed_reason='' WHERE id=?`, id)
	return affected(r, err)
}

// SetCaseAutoOff 는 이 사안을 자동 처리에서 빼거나 다시 넣는다.
func (s *Store) SetCaseAutoOff(ctx context.Context, id int64, off bool) error {
	v := 0
	if off {
		v = 1
	}
	r, err := s.db.ExecContext(ctx, `UPDATE cases SET auto_off=? WHERE id=?`, v, id)
	return affected(r, err)
}

// SetAttention 은 사람이 봐야 하는 이유를 적는다. 빈 문자열이면 지운다.
func (s *Store) SetAttention(ctx context.Context, id int64, text string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE cases SET attention=? WHERE id=?`, text, id)
	return affected(r, err)
}

func affected(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- submissions ----

// CreateSubmission 은 사안의 다음 회차 초안 행을 만든다.
func (s *Store) CreateSubmission(ctx context.Context, caseID int64, kind, draftJSON, title, body, asksJSON string) (Submission, error) {
	if asksJSON == "" {
		asksJSON = "[]"
	}
	var round int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(round),0)+1 FROM submissions WHERE case_id=?`, caseID).Scan(&round); err != nil {
		return Submission{}, err
	}
	r, err := s.db.ExecContext(ctx,
		`INSERT INTO submissions(case_id,round,kind,draft_json,title,body,asks_json,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		caseID, round, kind, draftJSON, title, body, asksJSON, s.ts())
	if err != nil {
		return Submission{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return Submission{}, err
	}
	return s.GetSubmission(ctx, id)
}

const submissionCols = `id,case_id,round,kind,draft_json,title,body,asks_json,status,receipt_no,submitted_at,created_at`

type scanner interface{ Scan(dest ...any) error }

func scanSubmission(r scanner) (Submission, error) {
	var x Submission
	var sub, created string
	err := r.Scan(&x.ID, &x.CaseID, &x.Round, &x.Kind, &x.DraftJSON, &x.Title, &x.Body, &x.AsksJSON,
		&x.Status, &x.ReceiptNo, &sub, &created)
	x.SubmittedAt = parseTime(sub)
	x.CreatedAt = parseTime(created)
	return x, err
}

func (s *Store) GetSubmission(ctx context.Context, id int64) (Submission, error) {
	x, err := scanSubmission(s.db.QueryRowContext(ctx, `SELECT `+submissionCols+` FROM submissions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

func (s *Store) ListSubmissions(ctx context.Context, caseID int64) ([]Submission, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+submissionCols+` FROM submissions WHERE case_id=? ORDER BY round`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Submission
	for rows.Next() {
		x, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SaveSubmissionText 는 사용자가 확정한 제목, 본문, 질의목록을 저장한다. 제출 전에만 가능하다.
func (s *Store) SaveSubmissionText(ctx context.Context, id int64, title, body, asksJSON string) error {
	r, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET title=?, body=?, asks_json=? WHERE id=? AND status='draft'`,
		title, body, asksJSON, id)
	return affected(r, err)
}

// MarkSubmitted 는 사용자가 사이트에 제출한 뒤 접수번호를 기록한다.
func (s *Store) MarkSubmitted(ctx context.Context, id int64, receiptNo string) error {
	r, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET status='submitted', receipt_no=?, submitted_at=? WHERE id=? AND status='draft'`,
		receiptNo, s.ts(), id)
	return affected(r, err)
}

// DeleteDraft 는 제출 전 초안을 지운다.
func (s *Store) DeleteDraft(ctx context.Context, id int64) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM submissions WHERE id=? AND status='draft'`, id)
	return affected(r, err)
}

// CountSubmittedSince 는 since 이후 제출한 건수다. 하루 한도 점검에 쓴다.
func (s *Store) CountSubmittedSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM submissions WHERE submitted_at != '' AND submitted_at >= ?`,
		since.UTC().Format(timeLayout)).Scan(&n)
	return n, err
}

// LastSubmittedAt 은 가장 최근 제출 시각이다. 없으면 zero time.
func (s *Store) LastSubmittedAt(ctx context.Context) (time.Time, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(submitted_at) FROM submissions WHERE submitted_at != ''`).Scan(&v)
	if err != nil || !v.Valid {
		return time.Time{}, err
	}
	return parseTime(v.String), nil
}

// ---- answers / evaluations ----

// CreateAnswer 는 사용자가 붙여넣은 답변을 기록한다.
func (s *Store) CreateAnswer(ctx context.Context, submissionID int64, body, dept string) (Answer, error) {
	return s.CreateAnswerFrom(ctx, submissionID, body, dept, "pasted")
}

// CreateAnswerFrom 은 출처(pasted|portal)를 지정해 답변을 기록한다.
func (s *Store) CreateAnswerFrom(ctx context.Context, submissionID int64, body, dept, source string) (Answer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Answer{}, err
	}
	defer tx.Rollback()
	now := s.ts()
	r, err := tx.ExecContext(ctx,
		`INSERT INTO answers(submission_id,body,dept,received_at,source) VALUES(?,?,?,?,?)`,
		submissionID, body, dept, now, source)
	if err != nil {
		return Answer{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return Answer{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE submissions SET status='answered' WHERE id=? AND status IN ('submitted','answered')`, submissionID); err != nil {
		return Answer{}, err
	}
	if err := tx.Commit(); err != nil {
		return Answer{}, err
	}
	return Answer{ID: id, SubmissionID: submissionID, Body: body, Dept: dept, ReceivedAt: parseTime(now), Source: source}, nil
}

const answerCols = `a.id,a.submission_id,a.body,a.dept,a.received_at,a.source,a.processed_at`

func scanAnswer(r scanner) (Answer, error) {
	var a Answer
	var rec, proc string
	err := r.Scan(&a.ID, &a.SubmissionID, &a.Body, &a.Dept, &rec, &a.Source, &proc)
	a.ReceivedAt = parseTime(rec)
	a.ProcessedAt = parseTime(proc)
	return a, err
}

func (s *Store) GetAnswer(ctx context.Context, id int64) (Answer, error) {
	a, err := scanAnswer(s.db.QueryRowContext(ctx, `SELECT `+answerCols+` FROM answers a WHERE a.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) queryAnswers(ctx context.Context, q string, args ...any) ([]Answer, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Answer
	for rows.Next() {
		a, err := scanAnswer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ListAnswers(ctx context.Context, submissionID int64) ([]Answer, error) {
	return s.queryAnswers(ctx,
		`SELECT `+answerCols+` FROM answers a WHERE a.submission_id=? ORDER BY a.id`, submissionID)
}

// ListPendingAnswers 는 아직 자동 처리하지 않은 답변이다. 종결되었거나 자동 처리를 끈 사안은 뺀다.
func (s *Store) ListPendingAnswers(ctx context.Context) ([]Answer, error) {
	return s.queryAnswers(ctx,
		`SELECT `+answerCols+` FROM answers a
		 JOIN submissions sub ON sub.id = a.submission_id
		 JOIN cases c ON c.id = sub.case_id
		 WHERE a.processed_at = '' AND c.status = 'open' AND c.auto_off = 0
		 ORDER BY a.id`)
}

// MarkAnswerProcessed 는 답변의 자동 처리를 마쳤다고 표시한다.
func (s *Store) MarkAnswerProcessed(ctx context.Context, id int64) error {
	r, err := s.db.ExecContext(ctx, `UPDATE answers SET processed_at=? WHERE id=?`, s.ts(), id)
	return affected(r, err)
}

// ListAwaitingSubmissions 는 제출했지만 답변을 기록하지 않은 건이다(사이트 점검 대상).
func (s *Store) ListAwaitingSubmissions(ctx context.Context) ([]Submission, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sub.id,sub.case_id,sub.round,sub.kind,sub.draft_json,sub.title,sub.body,sub.asks_json,
		        sub.status,sub.receipt_no,sub.submitted_at,sub.created_at
		 FROM submissions sub JOIN cases c ON c.id = sub.case_id
		 WHERE sub.status = 'submitted' AND sub.receipt_no != '' AND c.status = 'open' AND c.auto_off = 0
		 ORDER BY sub.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Submission
	for rows.Next() {
		x, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) SaveEvaluation(ctx context.Context, answerID int64, resultJSON string, score int, recommendation string) (Evaluation, error) {
	now := s.ts()
	r, err := s.db.ExecContext(ctx,
		`INSERT INTO evaluations(answer_id,result_json,score,recommendation,created_at) VALUES(?,?,?,?,?)`,
		answerID, resultJSON, score, recommendation, now)
	if err != nil {
		return Evaluation{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return Evaluation{}, err
	}
	return Evaluation{ID: id, AnswerID: answerID, ResultJSON: resultJSON, Score: score,
		Recommendation: recommendation, CreatedAt: parseTime(now)}, nil
}

// LatestEvaluation 은 답변의 가장 최근 평가다. 없으면 ErrNotFound.
func (s *Store) LatestEvaluation(ctx context.Context, answerID int64) (Evaluation, error) {
	var e Evaluation
	var created string
	err := s.db.QueryRowContext(ctx,
		`SELECT id,answer_id,result_json,score,recommendation,created_at FROM evaluations
		 WHERE answer_id=? ORDER BY id DESC LIMIT 1`, answerID).
		Scan(&e.ID, &e.AnswerID, &e.ResultJSON, &e.Score, &e.Recommendation, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	e.CreatedAt = parseTime(created)
	return e, err
}

// ---- events ----

func (s *Store) AddEvent(ctx context.Context, caseID int64, kind, detail string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events(case_id,kind,detail,at) VALUES(?,?,?,?)`, caseID, kind, detail, s.ts())
	return err
}

func (s *Store) ListEvents(ctx context.Context, caseID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,case_id,kind,detail,at FROM events WHERE case_id=? ORDER BY id DESC LIMIT 200`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.ID, &e.CaseID, &e.Kind, &e.Detail, &at); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- reports ----

func (s *Store) AddReport(ctx context.Context, r Report) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO reports(case_id,kind,title,body,created_at) VALUES(?,?,?,?,?)`,
		r.CaseID, r.Kind, r.Title, r.Body, s.ts())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanReport(r scanner) (Report, error) {
	var x Report
	var read int
	var at string
	err := r.Scan(&x.ID, &x.CaseID, &x.Kind, &x.Title, &x.Body, &read, &at)
	x.Read = read != 0
	x.CreatedAt = parseTime(at)
	return x, err
}

// ListReports 는 최근 보고부터 limit 개다.
func (s *Store) ListReports(ctx context.Context, limit int) ([]Report, error) {
	return s.queryReports(ctx, `SELECT id,case_id,kind,title,body,read,created_at FROM reports ORDER BY id DESC LIMIT ?`, limit)
}

// ReportsSince 는 since 이후의 보고를 오래된 순으로 돌려준다. 하루 요약에 쓴다.
func (s *Store) ReportsSince(ctx context.Context, since time.Time) ([]Report, error) {
	return s.queryReports(ctx,
		`SELECT id,case_id,kind,title,body,read,created_at FROM reports WHERE created_at >= ? ORDER BY id`,
		since.UTC().Format(timeLayout))
}

func (s *Store) queryReports(ctx context.Context, q string, args ...any) ([]Report, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		x, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) CountUnreadReports(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports WHERE read = 0`).Scan(&n)
	return n, err
}

func (s *Store) MarkReportsRead(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE reports SET read = 1 WHERE read = 0`)
	return err
}

// ---- settings / style ----

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) AddStyleSample(ctx context.Context, kind, text, original string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO style_samples(kind,text,original,created_at) VALUES(?,?,?,?)`, kind, text, original, s.ts())
	return err
}

// ListStyleSamples 는 최근 순으로 limit 개를 돌려준다. kind 가 비어 있으면 전부.
func (s *Store) ListStyleSamples(ctx context.Context, kind string, limit int) ([]StyleSample, error) {
	q := `SELECT id,kind,text,original,created_at FROM style_samples`
	var args []any
	if kind != "" {
		q += ` WHERE kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StyleSample
	for rows.Next() {
		var x StyleSample
		var at string
		if err := rows.Scan(&x.ID, &x.Kind, &x.Text, &x.Original, &at); err != nil {
			return nil, err
		}
		x.CreatedAt = parseTime(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) DeleteStyleSample(ctx context.Context, id int64) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM style_samples WHERE id=?`, id)
	return affected(r, err)
}
