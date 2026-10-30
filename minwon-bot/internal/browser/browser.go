// Package browser 는 사용자의 Chrome/Edge 를 전용 프로필로 열어 화면을 읽는다.
// 로그인은 사용자가 그 브라우저에서 직접 한다. 이 패키지는 로그인을 대신하지 않고,
// 자동화를 숨기려는 어떤 조작(지문 위조, webdriver 표시 숨기기 등)도 하지 않는다.
package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

type Options struct {
	// ProfileDir 는 브라우저 프로필 폴더다. 로그인 상태가 여기에 남으므로 본인만 읽을 수 있게 만든다.
	ProfileDir string
	// Bin 은 브라우저 실행 파일. 비우면 설치된 Chrome/Edge 를 찾는다.
	Bin string
	// Headless 는 창 없이 실행한다. 로그인은 사람이 해야 하므로 테스트에서만 쓴다.
	Headless bool
	// NoSandbox 는 root 로 실행하는 컨테이너 등에서만 쓴다.
	NoSandbox bool
}

type Session struct {
	b    *rod.Browser
	l    *launcher.Launcher
	exit chan struct{} // 브라우저 프로세스가 끝나면 닫힌다
}

// Find 는 이 PC 에 설치된 Chrome/Edge 를 찾는다. 내려받지 않는다.
func Find() (string, error) {
	if p, ok := launcher.LookPath(); ok {
		return p, nil
	}
	return "", errors.New("Chrome 또는 Edge 를 찾지 못했습니다. 설치한 뒤 다시 실행하거나, 실행 파일 경로를 직접 지정하세요")
}

// Launch 는 브라우저를 열고 연결한다.
func Launch(o Options) (*Session, error) {
	bin := o.Bin
	if bin == "" {
		var err error
		if bin, err = Find(); err != nil {
			return nil, err
		}
	}
	if o.ProfileDir == "" {
		return nil, errors.New("프로필 폴더가 필요합니다")
	}
	if err := os.MkdirAll(o.ProfileDir, 0o700); err != nil {
		return nil, err
	}
	// 브라우저가 연결 주소를 끝내 알려 주지 않으면(원격 제어를 막는 기관 PC 정책 등) 무한정 기다리지 않는다.
	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
	defer cancel()
	// Leakless 는 보조 실행 파일을 따로 띄워 백신이 오탐하는 일이 있어 끈다.
	l := launcher.New().Context(ctx).Bin(bin).UserDataDir(o.ProfileDir).Headless(o.Headless).Leakless(false)
	if o.NoSandbox {
		l = l.NoSandbox(true)
	}
	u, err := l.Launch()
	if err != nil {
		return nil, explainLaunch(err)
	}
	// rod 의 Cleanup 은 프로세스가 끝나길 기다린 뒤 UserDataDir 을 통째로 지운다. 로그인 상태가 든
	// 프로필을 지우면 안 되므로 경로를 빼 두고 "끝날 때까지 기다리기" 로만 쓴다(빈 경로는 지우지 않는다).
	l.Delete(flags.UserDataDir)
	exit := make(chan struct{})
	go func() {
		l.Cleanup()
		close(exit)
	}()
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("브라우저에 연결하지 못했습니다: %w", err)
	}
	return &Session{b: b, l: l, exit: exit}, nil
}

var launchTimeout = 60 * time.Second // 테스트에서 줄인다

func explainLaunch(err error) error {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("브라우저가 %d초 안에 준비되지 않았습니다. 회사·기관 PC 는 정책으로 브라우저 원격 제어를 막아 둔 경우가 있습니다: %w",
			int(launchTimeout.Seconds()), err)
	case strings.Contains(msg, "SingletonLock") || strings.Contains(msg, "ProcessSingleton") || strings.Contains(msg, "existing browser session"):
		return fmt.Errorf("같은 프로필의 저장용 브라우저가 이미 열려 있습니다. 그 창을 모두 닫고 다시 시도하세요: %w", err)
	case strings.Contains(msg, "debug url"):
		// Windows 에서는 같은 프로필의 브라우저가 이미 떠 있으면 그쪽에 창만 넘기고 아무 말 없이 끝난다.
		return fmt.Errorf("브라우저가 열리자마자 닫혔습니다. 저장용 브라우저 창이 이미 열려 있다면 닫고 다시 시도하세요: %w", err)
	}
	return fmt.Errorf("브라우저를 열지 못했습니다: %w", err)
}

// 주의: rod 는 탭 객체를 처음 만든 문맥 그대로 캐시한다. 시간 제한을 건 브라우저(b.Timeout)로 탭 목록을 읽거나
// 탭을 만들면 그 제한이 탭에 남아, 제한이 지난 뒤에는 그 탭을 읽지 못한다. 시간 제한은 탭 객체에만 건다.

// Open 은 주소를 연다. 처음 떠 있는 빈 탭이 있으면 그 탭을 쓰고, 불러오기가 끝나길 잠시 기다린다.
// 느리거나 응답이 없는 사이트여도 오류로 만들지 않는다(사용자가 화면을 보며 판단한다).
func (s *Session) Open(url string) (*rod.Page, error) {
	var page *rod.Page
	if pages, err := s.b.Pages(); err == nil {
		for _, p := range pages {
			if info, err := p.Timeout(5 * time.Second).Info(); err == nil && isBlank(info.URL) {
				page = p
				break
			}
		}
	}
	if page == nil {
		var err error
		if page, err = s.b.Page(rodPageOpts(url)); err != nil {
			return nil, err
		}
	} else if err := page.Timeout(20 * time.Second).Navigate(url); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return page, nil // 아직 불러오는 중이다. 사용자가 브라우저 화면을 보며 기다린다
	}
	_ = page.Timeout(20 * time.Second).WaitLoad()
	return page, nil
}

func isBlank(u string) bool {
	return u == "" || u == "about:blank" || strings.HasPrefix(u, "chrome://newtab") || strings.HasPrefix(u, "edge://newtab")
}

// Show 는 이미 열린 브라우저의 첫 탭을 앞으로 가져온다. 탭이 하나도 없으면(macOS 에서 창만 닫은 경우 등) url 을 연다.
func (s *Session) Show(url string) error {
	b := s.b.Timeout(checkTimeout)
	list, err := proto.TargetGetTargets{}.Call(b)
	if err != nil {
		return err
	}
	for _, t := range list.TargetInfos {
		if t.Type == proto.TargetTargetInfoTypePage {
			return proto.TargetActivateTarget{TargetID: t.TargetID}.Call(b)
		}
	}
	_, err = s.Open(url)
	return err
}

// Pages 는 열려 있는 탭이다.
func (s *Session) Pages() (rod.Pages, error) { return s.b.Pages() }

// Close 는 브라우저를 닫고 실제로 꺼질 때까지 잠시 기다린다. 프로필 폴더(로그인 상태)는 남는다.
// 닫으라는 요청에 응하지 않으면 강제로 끝낸다.
func (s *Session) Close() {
	_ = s.b.Timeout(5 * time.Second).Close()
	if s.waitExit(10 * time.Second) {
		return
	}
	s.l.Kill()
	s.waitExit(5 * time.Second)
}

func (s *Session) waitExit(d time.Duration) bool {
	select {
	case <-s.exit:
		return true
	case <-time.After(d):
		return false
	}
}

// Exited 는 브라우저 프로세스가 끝났는지다(사용자가 창을 모두 닫은 경우 등).
func (s *Session) Exited() bool {
	select {
	case <-s.exit:
		return true
	default:
		return false
	}
}

// Alive 는 브라우저가 아직 켜져 있고 응답하는지다. 사용자가 창을 직접 닫았으면 false.
func (s *Session) Alive() bool {
	if s.Exited() {
		return false
	}
	_, err := proto.BrowserGetVersion{}.Call(s.b.Timeout(checkTimeout))
	return err == nil
}

// checkTimeout 은 브라우저가 응답하는지 볼 때 기다리는 시간이다(테스트에서 줄인다).
var checkTimeout = 5 * time.Second
