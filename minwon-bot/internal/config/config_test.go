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
	}
	for name, mut := range cases {
		c := Default()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: 잘못된 값이 통과함", name)
		}
	}
}
