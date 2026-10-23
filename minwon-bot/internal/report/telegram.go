package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Telegram 은 텔레그램 봇 API 의 sendMessage 로 보고를 보낸다.
type Telegram struct {
	Token  string
	ChatID string
	Full   bool

	// 아래는 테스트에서만 바꾼다.
	BaseURL string
	Client  *http.Client
}

func (t *Telegram) Name() string { return "텔레그램" }

const telegramLimit = 4000 // API 한도는 4096자

func (t *Telegram) Send(ctx context.Context, r Report) error {
	text := r.External(t.Full)
	if rs := []rune(text); len(rs) > telegramLimit {
		text = string(rs[:telegramLimit]) + "…\n" + footer
	}
	payload, _ := json.Marshal(map[string]any{
		"chat_id": t.ChatID, "text": text, "disable_web_page_preview": true,
	})
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return errors.New("요청을 만들지 못했습니다")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// 요청 주소에 토큰이 들어 있으므로 주소가 담긴 오류 문구는 버리고 원인만 남긴다.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("전송 실패: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		if out.Description == "" {
			out.Description = resp.Status
		}
		return fmt.Errorf("텔레그램이 거절했습니다: %s", out.Description)
	}
	return nil
}
