CREATE TABLE IF NOT EXISTS cases (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  title         TEXT NOT NULL,
  agency        TEXT NOT NULL DEFAULT '',
  facts         TEXT NOT NULL DEFAULT '',
  desired       TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'open',   -- open | closed
  closed_reason TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS submissions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  case_id      INTEGER NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
  round        INTEGER NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'initial', -- initial | followup
  draft_json   TEXT NOT NULL DEFAULT '',        -- 초안 원본(AI 출력)
  title        TEXT NOT NULL DEFAULT '',        -- 사용자가 확정한 제목
  body         TEXT NOT NULL DEFAULT '',        -- 사용자가 확정한 본문
  asks_json    TEXT NOT NULL DEFAULT '[]',      -- 질의목록
  status       TEXT NOT NULL DEFAULT 'draft',   -- draft | submitted | answered
  receipt_no   TEXT NOT NULL DEFAULT '',
  submitted_at TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_submissions_case ON submissions(case_id);

CREATE TABLE IF NOT EXISTS answers (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  submission_id INTEGER NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
  body          TEXT NOT NULL,
  dept          TEXT NOT NULL DEFAULT '',
  received_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_answers_submission ON answers(submission_id);

CREATE TABLE IF NOT EXISTS evaluations (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  answer_id      INTEGER NOT NULL REFERENCES answers(id) ON DELETE CASCADE,
  result_json    TEXT NOT NULL,
  score          INTEGER NOT NULL,
  recommendation TEXT NOT NULL,
  created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_evaluations_answer ON evaluations(answer_id);

CREATE TABLE IF NOT EXISTS events (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  case_id INTEGER NOT NULL DEFAULT 0,
  kind    TEXT NOT NULL,
  detail  TEXT NOT NULL DEFAULT '',
  at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- 문체 학습용 글. kind=sample 은 사용자가 붙여넣은 글,
-- kind=edit 은 AI 초안(original)을 사용자가 고친 최종본(text).
CREATE TABLE IF NOT EXISTS style_samples (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT NOT NULL,
  text       TEXT NOT NULL,
  original   TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
