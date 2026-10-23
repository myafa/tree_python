// Package browser 는 사용자의 Chrome/Edge 를 전용 프로필로 열어 화면을 읽는다.
// 로그인은 사용자가 그 브라우저에서 직접 한다. 이 패키지는 로그인을 대신하지 않고,
// 자동화를 숨기려는 어떤 조작(지문 위조, webdriver 표시 숨기기 등)도 하지 않는다.
package browser

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
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
	b *rod.Browser
	l *launcher.Launcher
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
	// Leakless 는 보조 실행 파일을 따로 띄워 백신이 오탐하는 일이 있어 끈다.
	l := launcher.New().Bin(bin).UserDataDir(o.ProfileDir).Headless(o.Headless).Leakless(false)
	if o.NoSandbox {
		l = l.NoSandbox(true)
	}
	u, err := l.Launch()
	if err != nil {
		return nil, explainLaunch(err)
	}
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("브라우저에 연결하지 못했습니다: %w", err)
	}
	return &Session{b: b, l: l}, nil
}

func explainLaunch(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "already") || strings.Contains(msg, "SingletonLock") || strings.Contains(msg, "profile") {
		return fmt.Errorf("같은 프로필의 브라우저가 이미 열려 있는 것 같습니다. 열려 있는 브라우저를 모두 닫고 다시 실행하세요: %w", err)
	}
	return fmt.Errorf("브라우저를 열지 못했습니다: %w", err)
}

// Open 은 주소를 연다. 처음 떠 있는 빈 탭이 있으면 그 탭을 쓰고, 불러오기가 끝나길 잠시 기다린다.
// 느리거나 응답이 없는 사이트여도 오류로 만들지 않는다(사용자가 화면을 보며 판단한다).
func (s *Session) Open(url string) (*rod.Page, error) {
	var page *rod.Page
	if pages, err := s.b.Pages(); err == nil {
		for _, p := range pages {
			if info, err := p.Info(); err == nil && isBlank(info.URL) {
				page = p
				break
			}
		}
	}
	if page != nil {
		if err := page.Navigate(url); err != nil {
			return nil, err
		}
	} else {
		var err error
		if page, err = s.b.Page(rodPageOpts(url)); err != nil {
			return nil, err
		}
	}
	_ = page.Timeout(20 * time.Second).WaitLoad()
	return page, nil
}

func isBlank(u string) bool {
	return u == "" || u == "about:blank" || strings.HasPrefix(u, "chrome://newtab") || strings.HasPrefix(u, "edge://newtab")
}

// Pages 는 열려 있는 탭이다.
func (s *Session) Pages() (rod.Pages, error) { return s.b.Pages() }

// Close 는 브라우저를 닫는다. 프로필(로그인 상태)은 남는다.
func (s *Session) Close() {
	_ = s.b.Close()
	s.l.Cleanup()
}
