// Package llm 은 민원 작성과 평가에 쓰는 AI 호출을 하나의 인터페이스로 감싼다.
// 구독(claude -p)과 API 키 두 가지 백엔드가 있고, 모두 JSON 스키마를 만족하는 JSON 만 돌려준다.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Request 는 AI 호출 한 번이다. Schema 는 필수이며 출력은 항상 이 스키마의 JSON 이다.
type Request struct {
	System string
	User   string
	Schema map[string]any
}

type LLM interface {
	Generate(ctx context.Context, r Request) (json.RawMessage, error)
	// Name 은 화면에 보여줄 백엔드 이름이다.
	Name() string
}

var (
	// ErrRefusal 은 안전장치가 응답을 거절했다는 뜻이다. 재시도해도 같은 결과가 나올 수 있다.
	ErrRefusal = errors.New("AI 가 이 요청에 응답하지 않았습니다(안전장치). 표현을 바꾸거나 사실 위주로 다시 입력해 보세요")
	// ErrTruncated 는 응답이 길이 제한에 걸려 잘렸다는 뜻이다.
	ErrTruncated = errors.New("AI 응답이 길이 제한으로 잘렸습니다")
)

// Func 는 함수를 LLM 으로 쓰게 해 준다. 테스트에서 가짜 응답을 만들 때 쓴다.
type Func func(ctx context.Context, r Request) (json.RawMessage, error)

func (f Func) Generate(ctx context.Context, r Request) (json.RawMessage, error) { return f(ctx, r) }
func (f Func) Name() string                                                     { return "func" }

// ExtractJSON 은 모델 출력에서 JSON 객체를 꺼낸다. 코드 펜스나 앞뒤 설명이 붙어 있어도 처리한다.
func ExtractJSON(s string) (json.RawMessage, error) {
	s = strings.TrimSpace(s)
	if json.Valid([]byte(s)) {
		return json.RawMessage(s), nil
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	s = strings.TrimSpace(s)
	if json.Valid([]byte(s)) {
		return json.RawMessage(s), nil
	}
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		cand := s[i : j+1]
		if json.Valid([]byte(cand)) {
			return json.RawMessage(cand), nil
		}
	}
	return nil, errors.New("AI 응답에서 JSON 을 찾지 못했습니다")
}

// Decode 는 AI 가 준 JSON 을 구조체로 읽는다. 모르는 필드는 무시한다.
func Decode[T any](raw json.RawMessage) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&v); err != nil {
		return v, errors.New("AI 응답 형식이 맞지 않습니다: " + err.Error())
	}
	return v, nil
}

// ---- JSON Schema 도우미. 모든 속성을 required 로 두고 추가 속성을 막는다(구조화 출력 규칙). ----

func Obj(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	sort.Strings(req)
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             req,
		"additionalProperties": false,
	}
}

func Str() map[string]any  { return map[string]any{"type": "string"} }
func Int() map[string]any  { return map[string]any{"type": "integer"} }
func Bool() map[string]any { return map[string]any{"type": "boolean"} }
func Arr(item map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": item}
}
func StrArr() map[string]any { return Arr(Str()) }
func Enum(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
