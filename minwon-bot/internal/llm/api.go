package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// API 는 Anthropic API 를 직접 호출한다. 호출마다 토큰 단위로 과금된다.
// 인증은 ANTHROPIC_API_KEY 환경변수(또는 `ant auth login` 프로필)를 쓰고, 키를 파일에 저장하지 않는다.
type API struct {
	Model   string
	Effort  string
	Timeout time.Duration
	client  anthropic.Client
}

func NewAPI(model, effort string, timeout time.Duration, opts ...option.RequestOption) *API {
	return &API{Model: model, Effort: effort, Timeout: timeout, client: anthropic.NewClient(opts...)}
}

func (a *API) Name() string { return "Anthropic API (" + a.Model + ")" }

// 안전장치 거절 시 다른 모델로 자동 재시도(server-side fallback)를 지원하는 모델.
func supportsFallback(model string) bool {
	switch model {
	case "claude-opus-5-5", "claude-opus-5", "claude-fable-5-1", "claude-sonnet-5-5":
		return true
	}
	return false
}

func (a *API) Generate(ctx context.Context, r Request) (json.RawMessage, error) {
	if r.Schema == nil {
		return nil, errors.New("스키마가 필요합니다")
	}
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	p := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(a.Model),
		MaxTokens: 16000,
		System: []anthropic.BetaTextBlockParam{{
			Text:         r.System,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(r.User)),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(a.Effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: r.Schema},
		},
	}
	if supportsFallback(a.Model) {
		p.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}

	msg, err := a.client.Beta.Messages.New(ctx, p)
	if err != nil {
		return nil, explainAPIError(err)
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return nil, ErrRefusal
	case anthropic.BetaStopReasonMaxTokens:
		return nil, ErrTruncated
	}
	var text strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return ExtractJSON(text.String())
}

func explainAPIError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return errors.New("API 인증에 실패했습니다. ANTHROPIC_API_KEY 환경변수를 설정했는지 확인하세요")
		case 403:
			return errors.New("API 키에 이 모델을 쓸 권한이 없습니다")
		case 429:
			return errors.New("API 호출 한도를 넘었습니다. 잠시 뒤 다시 시도하세요")
		}
		return fmt.Errorf("API 오류(%d): %s", apiErr.StatusCode, apiErr.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("API 호출이 제한 시간 안에 끝나지 않았습니다")
	}
	return err
}
