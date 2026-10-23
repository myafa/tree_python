// Package portal 은 민원 사이트(국민신문고 등)와의 연동 인터페이스다.
// 로그인은 사용자가 직접 하며, 이 인터페이스는 로그인을 대신하지 않는다.
package portal

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrLoginRequired 는 사이트에 로그인되어 있지 않다는 뜻이다. 사용자가 다시 로그인해야 한다.
	ErrLoginRequired = errors.New("사이트에 로그인되어 있지 않습니다. 열려 있는 브라우저에서 다시 로그인하세요")
	// ErrChallenge 는 추가 인증이나 자동입력 방지 확인이 필요하다는 뜻이다. 우회하지 않고 멈춘다.
	ErrChallenge = errors.New("사이트가 추가 인증 또는 자동입력 방지 확인을 요구합니다. 직접 처리해야 합니다")
	// ErrUnexpectedPage 는 예상과 다른 화면이라 안전하게 진행할 수 없다는 뜻이다.
	// 제출이 반쯤 이루어졌을 수 있으므로 자동 재시도하지 않는다.
	ErrUnexpectedPage = errors.New("예상과 다른 화면이라 중단했습니다")
	// ErrNotConfigured 는 이 사이트의 연동이 아직 준비되지 않았다는 뜻이다.
	ErrNotConfigured = errors.New("이 사이트 연동은 아직 준비되지 않았습니다")
)

// Form 은 사이트에 제출할 민원이다.
type Form struct {
	Title   string
	Body    string
	Agency  string
	Private bool // 비공개로 제출
}

type Receipt struct {
	No          string
	SubmittedAt time.Time
}

// Answer 는 사이트에서 가져온 기관 답변이다.
type Answer struct {
	Body       string
	Dept       string
	AnsweredAt time.Time
}

type Portal interface {
	Name() string
	// EnsureLogin 은 로그인된 상태인지만 확인한다. 아니면 ErrLoginRequired.
	EnsureLogin(ctx context.Context) error
	// Submit 은 민원을 제출하고 접수번호를 돌려준다. 접수번호를 확인하지 못하면
	// 제출 여부가 불확실하므로 반드시 오류(ErrUnexpectedPage 등)로 돌려줘야 한다.
	Submit(ctx context.Context, f Form) (Receipt, error)
	// FetchAnswer 는 접수번호의 답변을 가져온다. 아직 답변이 없으면 (nil, nil).
	FetchAnswer(ctx context.Context, receiptNo string) (*Answer, error)
}
