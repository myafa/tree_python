// Package config 는 사용자 PC에 저장되는 설정 파일을 읽고 쓴다.
// API 키는 파일에 저장하지 않고 환경변수(ANTHROPIC_API_KEY)만 사용한다.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	BackendClaudeCode = "claude_code"
	BackendAPI        = "api"
)

type LLM struct {
	// Backend 는 "claude_code"(구독, 기본) 또는 "api"(API 키, 토큰 과금).
	Backend string `json:"backend"`
	// Model 은 claude_code 에서는 별칭("opus" 등), api 에서는 모델 ID.
	Model string `json:"model"`
	// Effort 는 low|medium|high|xhigh|max. api 백엔드에서만 쓰인다.
	Effort string `json:"effort"`
	// ClaudeBin 은 claude 실행 파일 경로. 비우면 PATH 에서 찾는다.
	ClaudeBin string `json:"claude_bin"`
	// TimeoutSec 은 호출 1회의 제한 시간.
	TimeoutSec int `json:"timeout_sec"`
}

type Limits struct {
	// DailyMax 는 하루 최대 제출 건수.
	DailyMax int `json:"daily_max"`
	// MinGapMinutes 는 제출 사이 최소 간격(분).
	MinGapMinutes int `json:"min_gap_minutes"`
	// RepeatRound 는 이 회차부터 반복민원으로 보고 자동 처리를 막는다.
	RepeatRound int `json:"repeat_round"`
	// Similarity 는 이전 회차와의 유사도 경고 기준(0~1).
	Similarity float64 `json:"similarity"`
}

// Auto 는 답변이 기록된 뒤 사람 승인 없이 처리하는 방식이다.
type Auto struct {
	// Enabled 가 꺼지면 주기 점검과 자동 처리를 모두 하지 않는다.
	Enabled bool `json:"enabled"`
	// AnswerAuto 는 답변이 기록되면 승인 없이 평가하고 재민원 또는 종결까지 처리한다.
	AnswerAuto bool `json:"answer_auto"`
	// CheckIntervalHours 는 사이트의 답변을 확인하는 간격(시간). 사이트 연동이 있을 때만 쓰인다.
	CheckIntervalHours int `json:"check_interval_hours"`
	// HoldPolicy 는 AI 가 판단을 보류한 사안의 처리: "notify"(보고만 하고 열어 둠) 또는 "close"(종결).
	HoldPolicy string `json:"hold_policy"`
	// DailySummaryHour 는 하루 요약을 보낼 시각(0~23). -1 이면 보내지 않는다.
	DailySummaryHour int `json:"daily_summary_hour"`
}

const (
	HoldNotify = "notify"
	HoldClose  = "close"
)

type Telegram struct {
	Token  string `json:"token"`
	ChatID string `json:"chat_id"`
}

type Email struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"`
	// TLS 는 "starttls"(587), "tls"(465), "none" 중 하나.
	TLS string `json:"tls"`
}

// Report 는 사후보고를 받을 곳이다. 웹 화면의 보고함은 항상 켜져 있다.
type Report struct {
	Telegram Telegram `json:"telegram"`
	Email    Email    `json:"email"`
	// Detail 은 텔레그램·이메일 같은 외부 채널에 보내는 내용의 양이다.
	// "summary"(기본): 사안 번호와 처리 결과만. 민원 내용과 답변은 보내지 않는다.
	// "full": 민원 본문과 답변까지 보낸다. 외부 서비스에 내용이 남으므로 직접 켜야 한다.
	// 웹 화면의 보고함에는 항상 전체 내용이 저장된다.
	Detail string `json:"detail"`
}

type Config struct {
	LLM    LLM    `json:"llm"`
	Limits Limits `json:"limits"`
	Auto   Auto   `json:"auto"`
	Report Report `json:"report"`
	// MaskTerms 는 AI 에 보내기 전에 가릴 낱말(본인 이름, 부대명 등).
	MaskTerms []string `json:"mask_terms"`
}

// TelegramToken 은 환경변수 MINWON_TELEGRAM_TOKEN 이 있으면 그것을, 없으면 설정 파일의 값을 쓴다.
func (c Config) TelegramToken() string {
	if v := os.Getenv("MINWON_TELEGRAM_TOKEN"); v != "" {
		return v
	}
	return c.Report.Telegram.Token
}

// EmailPassword 는 환경변수 MINWON_SMTP_PASSWORD 가 있으면 그것을, 없으면 설정 파일의 값을 쓴다.
func (c Config) EmailPassword() string {
	if v := os.Getenv("MINWON_SMTP_PASSWORD"); v != "" {
		return v
	}
	return c.Report.Email.Password
}

// TelegramEnabled, EmailEnabled 는 보고 채널이 쓸 수 있게 설정되었는지다.
func (c Config) TelegramEnabled() bool {
	return c.TelegramToken() != "" && strings.TrimSpace(c.Report.Telegram.ChatID) != ""
}

func (c Config) EmailEnabled() bool {
	e := c.Report.Email
	return e.Host != "" && e.To != "" && e.From != ""
}

// Default 는 처음 실행할 때 쓰는 기본값이다.
func Default() Config {
	return Config{
		LLM: LLM{
			Backend:    BackendClaudeCode,
			Model:      "opus",
			Effort:     "high",
			TimeoutSec: 300,
		},
		Limits: Limits{
			DailyMax:      3,
			MinGapMinutes: 10,
			RepeatRound:   3,
			Similarity:    0.6,
		},
		Auto: Auto{
			Enabled:            true,
			AnswerAuto:         true,
			CheckIntervalHours: 6,
			HoldPolicy:         HoldNotify,
			DailySummaryHour:   21,
		},
		Report: Report{Email: Email{Port: 587, TLS: "starttls"}, Detail: "summary"},
	}
}

// Validate 는 값이 쓸 수 있는 범위인지 확인한다.
func (c Config) Validate() error {
	switch c.LLM.Backend {
	case BackendClaudeCode, BackendAPI:
	default:
		return fmt.Errorf("llm.backend 는 %q 또는 %q 이어야 합니다", BackendClaudeCode, BackendAPI)
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		return errors.New("llm.model 이 비어 있습니다")
	}
	if c.LLM.Backend == BackendAPI && !strings.HasPrefix(strings.TrimSpace(c.LLM.Model), "claude-") {
		return errors.New("API 방식의 llm.model 은 claude-opus-5-5 처럼 모델 ID 로 입력하세요")
	}
	switch c.LLM.Effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return errors.New("llm.effort 는 low|medium|high|xhigh|max 중 하나여야 합니다")
	}
	if c.LLM.TimeoutSec < 10 || c.LLM.TimeoutSec > 1800 {
		return errors.New("llm.timeout_sec 는 10~1800 이어야 합니다")
	}
	if c.Limits.DailyMax < 1 || c.Limits.DailyMax > 20 {
		return errors.New("limits.daily_max 는 1~20 이어야 합니다")
	}
	if c.Limits.MinGapMinutes < 1 {
		return errors.New("limits.min_gap_minutes 는 1 이상이어야 합니다")
	}
	if c.Limits.RepeatRound < 2 {
		return errors.New("limits.repeat_round 는 2 이상이어야 합니다")
	}
	if c.Limits.Similarity <= 0 || c.Limits.Similarity > 1 {
		return errors.New("limits.similarity 는 0 초과 1 이하여야 합니다")
	}
	if c.Auto.CheckIntervalHours < 1 || c.Auto.CheckIntervalHours > 24 {
		return errors.New("auto.check_interval_hours 는 1~24 이어야 합니다")
	}
	if c.Auto.HoldPolicy != HoldNotify && c.Auto.HoldPolicy != HoldClose {
		return fmt.Errorf("auto.hold_policy 는 %q 또는 %q 이어야 합니다", HoldNotify, HoldClose)
	}
	if c.Auto.DailySummaryHour < -1 || c.Auto.DailySummaryHour > 23 {
		return errors.New("auto.daily_summary_hour 는 -1(끔) 또는 0~23 이어야 합니다")
	}
	if c.Report.Detail != "summary" && c.Report.Detail != "full" {
		return errors.New("report.detail 은 summary 또는 full 이어야 합니다")
	}
	if (c.Report.Telegram.Token != "") != (strings.TrimSpace(c.Report.Telegram.ChatID) != "") && os.Getenv("MINWON_TELEGRAM_TOKEN") == "" {
		return errors.New("텔레그램은 토큰과 채팅 ID 를 둘 다 입력하거나 둘 다 비워야 합니다")
	}
	if e := c.Report.Email; e.Host != "" {
		if e.Port < 1 || e.Port > 65535 {
			return errors.New("report.email.port 는 1~65535 이어야 합니다")
		}
		if e.From == "" || e.To == "" {
			return errors.New("이메일은 보내는 주소와 받는 주소가 모두 필요합니다")
		}
		switch e.TLS {
		case "starttls", "tls", "none":
		default:
			return errors.New("report.email.tls 는 starttls|tls|none 중 하나여야 합니다")
		}
	}
	return nil
}

// Dir 은 설정과 DB 를 두는 폴더다. MINWON_HOME 으로 바꿀 수 있다.
func Dir() (string, error) {
	if d := os.Getenv("MINWON_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "minwon-bot"), nil
}

// Load 는 dir/config.json 을 읽는다. 파일이 없으면 기본값을 돌려준다.
func Load(dir string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config.json 해석 실패: %w", err)
	}
	return c, c.Validate()
}

// Save 는 검증한 뒤 dir/config.json 에 쓴다. 개인정보가 들어 있으므로 0600.
func Save(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "config.json"))
}
