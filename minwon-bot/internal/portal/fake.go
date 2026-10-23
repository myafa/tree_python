package portal

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Fake 는 테스트와 시연용 메모리 구현이다. 실제 사이트에 접속하지 않는다.
type Fake struct {
	mu sync.Mutex

	LoggedIn bool
	// Challenge 가 true 면 Submit 이 ErrChallenge 를 돌려준다.
	Challenge bool
	// SubmitErr 가 있으면 Submit 이 이 오류를 돌려준다.
	SubmitErr error
	// FetchErr 가 있으면 FetchAnswer 가 이 오류를 돌려준다.
	FetchErr error
	// Answers 는 접수번호별로 미리 준비한 답변이다.
	Answers map[string]*Answer

	Submitted []Form
	next      int
}

func NewFake() *Fake { return &Fake{LoggedIn: true, Answers: map[string]*Answer{}} }

func (f *Fake) Name() string { return "가짜 사이트" }

func (f *Fake) EnsureLogin(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.LoggedIn {
		return ErrLoginRequired
	}
	return nil
}

func (f *Fake) Submit(_ context.Context, form Form) (Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.LoggedIn {
		return Receipt{}, ErrLoginRequired
	}
	if f.Challenge {
		return Receipt{}, ErrChallenge
	}
	if f.SubmitErr != nil {
		return Receipt{}, f.SubmitErr
	}
	f.next++
	f.Submitted = append(f.Submitted, form)
	return Receipt{No: fmt.Sprintf("FAKE-%04d", f.next), SubmittedAt: time.Now()}, nil
}

func (f *Fake) FetchAnswer(_ context.Context, receiptNo string) (*Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.LoggedIn {
		return nil, ErrLoginRequired
	}
	if f.FetchErr != nil {
		return nil, f.FetchErr
	}
	return f.Answers[receiptNo], nil
}

// SetAnswer 는 접수번호에 기관 답변이 도착한 것처럼 만든다.
func (f *Fake) SetAnswer(receiptNo, body, dept string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Answers[receiptNo] = &Answer{Body: body, Dept: dept, AnsweredAt: time.Now()}
}

// SubmittedCount 는 지금까지 제출된 건수다.
func (f *Fake) SubmittedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Submitted)
}
