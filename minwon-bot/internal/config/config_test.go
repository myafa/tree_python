package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsDefault(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Backend != BackendClaudeCode || c.Limits.DailyMax != 3 {
		t.Fatalf("기본값이 아님: %+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := Default()
	c.LLM.Backend = BackendAPI
	c.LLM.Model = "claude-opus-5-5"
	c.MaskTerms = []string{"홍길동"}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.Model != "claude-opus-5-5" || len(got.MaskTerms) != 1 {
		t.Fatalf("왕복 실패: %+v", got)
	}
	st, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("권한이 0600 이 아님: %v", st.Mode().Perm())
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Config){
		"backend":    func(c *Config) { c.LLM.Backend = "x" },
		"effort":     func(c *Config) { c.LLM.Effort = "ultra" },
		"api alias":  func(c *Config) { c.LLM.Backend = BackendAPI; c.LLM.Model = "opus" },
		"daily":      func(c *Config) { c.Limits.DailyMax = 0 },
		"gap":        func(c *Config) { c.Limits.MinGapMinutes = 0 },
		"repeat":     func(c *Config) { c.Limits.RepeatRound = 1 },
		"similarity": func(c *Config) { c.Limits.Similarity = 0 },
		"timeout":    func(c *Config) { c.LLM.TimeoutSec = 1 },
		"detail":     func(c *Config) { c.Report.Detail = "all" },
		"interval":   func(c *Config) { c.Auto.CheckIntervalHours = 0 },
		"hold":       func(c *Config) { c.Auto.HoldPolicy = "ignore" },
		"summary":    func(c *Config) { c.Auto.DailySummaryHour = 24 },
		"tg half":    func(c *Config) { c.Report.Telegram.Token = "t" },
		"email port": func(c *Config) { c.Report.Email.Host = "smtp.x"; c.Report.Email.Port = 0 },
		"email to":   func(c *Config) { c.Report.Email.Host = "smtp.x"; c.Report.Email.From = "a@x" },
		"email tls": func(c *Config) {
			c.Report.Email.Host = "smtp.x"
			c.Report.Email.From = "a@x"
			c.Report.Email.To = "b@x"
			c.Report.Email.TLS = "ssl"
		},
	}
	for name, mut := range cases {
		c := Default()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: 잘못된 값이 통과함", name)
		}
	}
}

func TestOldConfigFileKeepsNewDefaults(t *testing.T) {
	dir := t.TempDir()
	// 자동 처리·보고 항목이 없던 예전 설정 파일
	old := `{"llm":{"backend":"claude_code","model":"opus","effort":"high","timeout_sec":300},` +
		`"limits":{"daily_max":2,"min_gap_minutes":10,"repeat_round":3,"similarity":0.6},"mask_terms":["이름:홍길동"]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits.DailyMax != 2 || !c.Auto.Enabled || c.Auto.CheckIntervalHours != 6 || c.Report.Email.TLS != "starttls" || c.Report.Detail != "summary" {
		t.Fatalf("예전 파일은 새 항목의 기본값을 유지해야 함: %+v", c)
	}
}

func TestSecretsFromEnvAndEnabled(t *testing.T) {
	c := Default()
	if c.TelegramEnabled() || c.EmailEnabled() {
		t.Fatal("기본값은 보고 채널이 꺼져 있어야 함")
	}
	c.Report.Telegram = Telegram{Token: "file-token", ChatID: "123"}
	if !c.TelegramEnabled() || c.TelegramToken() != "file-token" {
		t.Fatal("파일 값 사용")
	}
	t.Setenv("MINWON_TELEGRAM_TOKEN", "env-token")
	if c.TelegramToken() != "env-token" {
		t.Fatal("환경변수가 우선")
	}
	t.Setenv("MINWON_SMTP_PASSWORD", "pw")
	c.Report.Email = Email{Host: "h", Port: 587, From: "a@x", To: "b@x", TLS: "starttls", Password: "file-pw"}
	if !c.EmailEnabled() || c.EmailPassword() != "pw" {
		t.Fatal("SMTP 비밀번호는 환경변수가 우선")
	}
}
