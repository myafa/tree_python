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
	return &Store{db: db, now: time.Now}, nil
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

func (s *Store) GetCase(ctx context.Context, id int64) (Case, error) {
	var c Case
	var created string
	err := s.db.QueryRowContext(ctx,
		`SELECT id,title,agency,facts,desired,status,closed_reason,created_at FROM cases WHERE id=?`, id).
		Scan(&c.ID, &c.Title, &c.Agency, &c.Facts, &c.Desired, &c.Status, &c.ClosedReason, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CreatedAt = parseTime(created)
	return c, err
}

func (s *Store) ListCases(ctx context.Context) ([]Case, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,title,agency,facts,desired,status,closed_reason,created_at FROM cases ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Case
	for rows.Next() {
		var c Case
		var created string
		if err := rows.Scan(&c.ID, &c.Title, &c.Agency, &c.Facts, &c.Desired, &c.Status, &c.ClosedReason, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// AppendCaseFacts 는 사실관계 끝에 text 를 덧붙인다. 되묻기에 답한 내용을 남길 때 쓴다.
func (s *Store) AppendCaseFacts(ctx context.Context, id int64, text string) error {
	r, err := s.db.ExecContext(ctx,
		`UPDATE cases SET facts = CASE WHEN facts = '' THEN ? ELSE facts || char(10) || ? END WHERE id=?`, text, text, id)
	return affected(r, err)
}

func (s *Store) CloseCase(ctx context.Context, id int64, reason string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE cases SET status='closed', closed_reason=? WHERE id=?`, reason, id)
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

func (s *Store) CreateAnswer(ctx context.Context, submissionID int64, body, dept string) (Answer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Answer{}, err
	}
	defer tx.Rollback()
	now := s.ts()
	r, err := tx.ExecContext(ctx,
		`INSERT INTO answers(submission_id,body,dept,received_at) VALUES(?,?,?,?)`, submissionID, body, dept, now)
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
	return Answer{ID: id, SubmissionID: submissionID, Body: body, Dept: dept, ReceivedAt: parseTime(now)}, nil
}

func (s *Store) GetAnswer(ctx context.Context, id int64) (Answer, error) {
	var a Answer
	var rec string
	err := s.db.QueryRowContext(ctx,
		`SELECT id,submission_id,body,dept,received_at FROM answers WHERE id=?`, id).
		Scan(&a.ID, &a.SubmissionID, &a.Body, &a.Dept, &rec)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.ReceivedAt = parseTime(rec)
	return a, err
}

func (s *Store) ListAnswers(ctx context.Context, submissionID int64) ([]Answer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,submission_id,body,dept,received_at FROM answers WHERE submission_id=? ORDER BY id`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Answer
	for rows.Next() {
		var a Answer
		var rec string
		if err := rows.Scan(&a.ID, &a.SubmissionID, &a.Body, &a.Dept, &rec); err != nil {
			return nil, err
		}
		a.ReceivedAt = parseTime(rec)
		out = append(out, a)
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
