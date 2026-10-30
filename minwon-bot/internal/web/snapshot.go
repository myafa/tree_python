package web

// 국민신문고 화면 저장: 사이트 연동(자동 제출·답변 확인)을 만들 때 필요한 화면 구조를 사용자가 이 화면에서
// 버튼만 눌러 모으게 한다. 로그인은 열린 브라우저에서 사용자가 직접 하고, 이 기능은 로그인을 대신하지 않는다.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"minwon-bot/internal/browser"
	"minwon-bot/internal/privacy"
)

const snapDefaultURL = "https://www.epeople.go.kr/"

// snapState 는 저장용 브라우저 하나와 지금까지 모은 화면이다. 모은 화면은 브라우저를 닫아도 남고,
// '새로 시작' 을 누르거나 앱을 끌 때까지 같은 폴더에 이어서 쌓인다.
type snapState struct {
	mu      sync.Mutex
	sess    *browser.Session
	dir     string // 저장 폴더. 비어 있으면 다음 저장 때 새로 정한다
	started time.Time
	masker  *privacy.Masker
	terms   []string // masker 를 만든 가릴 낱말. 설정이 바뀌면 다시 만든다
	shots   []snapShot
	text    strings.Builder
}

type snapShot struct {
	Name     string   `json:"name"` // 저장한 폴더 이름(001-제목)
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Frames   int      `json:"frames"`
	Warnings []string `json:"warnings,omitempty"`
}

type snapStatus struct {
	Open  bool       `json:"open"`
	Dir   string     `json:"dir,omitempty"`
	Shots []snapShot `json:"shots"`
	Text  string     `json:"text"`
	Saved int        `json:"saved,omitempty"` // 방금 저장한 화면 수
}

func (st *snapState) status() snapStatus {
	shots := st.shots
	if shots == nil {
		shots = []snapShot{}
	}
	return snapStatus{Open: st.sess != nil, Dir: st.dir, Shots: shots, Text: st.text.String()}
}

// closeLocked 는 브라우저를 닫는다(프로필의 로그인 상태는 남는다). st.mu 를 잡고 부른다.
func (st *snapState) closeLocked() {
	if st.sess != nil {
		st.sess.Close()
		st.sess = nil
	}
}

func (s *Server) snapStatus() snapStatus {
	s.snap.mu.Lock()
	defer s.snap.mu.Unlock()
	return s.snap.status()
}

// Close 는 서버가 연 저장용 브라우저를 닫는다. 앱을 끝낼 때 부른다.
func (s *Server) Close() {
	s.snap.mu.Lock()
	defer s.snap.mu.Unlock()
	s.snap.closeLocked()
}

func (s *Server) browserOptions() browser.Options {
	var o browser.Options
	if s.d.Browser != nil {
		o = s.d.Browser()
	}
	o.ProfileDir = filepath.Join(s.d.DataDir, "browser-profile")
	return o
}

func checkSnapURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return snapDefaultURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", bad("주소는 http:// 또는 https:// 로 시작해야 합니다")
	}
	return u.String(), nil
}

func (s *Server) apiSnapStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	target, err := checkSnapURL(req.URL)
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.d.DataDir == "" {
		s.fail(w, conflict("데이터 폴더가 정해지지 않아 화면 저장을 쓸 수 없습니다"))
		return
	}
	st := &s.snap
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sess != nil {
		if st.sess.Alive() {
			if err := st.sess.Show(target); err != nil {
				s.fail(w, fmt.Errorf("열린 브라우저를 앞으로 가져오지 못했습니다: %w", err))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"reused": true, "status": st.status()})
			return
		}
		// 사용자가 창을 직접 닫았다. 끊긴 연결은 버리고 새로 띄운다.
		st.closeLocked()
	}
	sess, err := browser.Launch(s.browserOptions())
	if err != nil {
		s.fail(w, &userErr{http.StatusBadGateway, err.Error()})
		return
	}
	st.sess = sess
	if _, err := sess.Open(target); err != nil {
		// 브라우저는 떴으니 닫지 않는다. 사용자가 그 창의 주소창에 직접 입력해도 된다.
		s.fail(w, &userErr{http.StatusBadGateway, "브라우저는 열었지만 주소를 열지 못했습니다(" + err.Error() + "). 열린 창의 주소창에 직접 입력해도 됩니다"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reused": false, "status": st.status()})
}

func (s *Server) apiSnapCapture(w http.ResponseWriter, r *http.Request) {
	st := &s.snap
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sess == nil {
		s.fail(w, conflict("먼저 [브라우저 열기]를 누르세요"))
		return
	}
	// 프로세스가 끝났을 때만 닫힌 것으로 본다. 잠깐 응답이 늦을 뿐인 브라우저를 닫으면 로그인한 화면을 잃는다.
	if st.sess.Exited() {
		st.closeLocked()
		s.fail(w, conflict("저장용 브라우저가 닫혀 있습니다. [브라우저 열기]를 다시 누르세요. 지금까지 저장한 화면은 그대로 있습니다"))
		return
	}
	if st.dir == "" {
		st.started = s.d.Now()
		st.dir = filepath.Join(s.d.DataDir, "snapshots", st.started.Format("20060102-150405"))
	}
	terms := s.config().MaskTerms
	if st.masker == nil || !slices.Equal(terms, st.terms) {
		st.masker, st.terms = privacy.NewMasker(terms), slices.Clone(terms)
	}
	// 화면을 읽는 동안 사용자가 이 화면을 닫아도 저장은 끝까지 한다.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	shots, err := st.sess.Snapshot(ctx, browser.SnapshotOptions{OutDir: st.dir, Masker: st.masker})
	if err != nil && len(shots) == 0 {
		if st.sess.Exited() {
			st.closeLocked()
			s.fail(w, conflict("저장하는 사이 저장용 브라우저가 닫혔습니다. [브라우저 열기]를 다시 누르세요"))
			return
		}
		s.fail(w, fmt.Errorf("화면을 저장하지 못했습니다. 잠시 뒤 다시 누르고, 계속되면 [브라우저 열기]를 다시 누르세요: %w", err))
		return
	}
	if len(shots) == 0 {
		s.fail(w, conflict("저장할 화면이 없습니다. 저장용 브라우저에서 화면을 연 뒤 다시 누르세요"))
		return
	}
	if st.text.Len() == 0 {
		fmt.Fprintf(&st.text, "# 민원 도우미 화면 구조 요약 (버전 %s, %s 시작)\n\n", s.version(), st.started.Format("2006-01-02 15:04"))
	}
	for _, sh := range shots {
		name := filepath.Base(sh.Dir)
		st.shots = append(st.shots, snapShot{Name: name, Title: sh.Title, URL: sh.URL, Frames: len(sh.Frames), Warnings: sh.Warnings})
		fmt.Fprintf(&st.text, "######## %s\n%s\n", name, sh.Outline)
	}
	res := st.status()
	res.Saved = len(shots)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) apiSnapStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.snapStatus())
}

func (s *Server) apiSnapStop(w http.ResponseWriter, r *http.Request) {
	st := &s.snap
	st.mu.Lock()
	defer st.mu.Unlock()
	st.closeLocked()
	writeJSON(w, http.StatusOK, st.status())
}

// apiSnapReset 은 모은 화면 목록을 비운다. 다음 저장부터 새 폴더에 쌓는다. 이미 저장한 파일은 지우지 않는다.
func (s *Server) apiSnapReset(w http.ResponseWriter, r *http.Request) {
	st := &s.snap
	st.mu.Lock()
	defer st.mu.Unlock()
	st.dir, st.shots, st.masker, st.terms = "", nil, nil, nil
	st.text.Reset()
	writeJSON(w, http.StatusOK, st.status())
}

// apiSnapMask 는 로그인하면 화면에 보이는 이름을 설정의 가릴 낱말에 더한다. 이름은 형식으로 찾을 수 없어서다.
func (s *Server) apiSnapMask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(name); n < 2 || n > 30 || strings.ContainsAny(name, "\r\n:：") {
		// 한 글자는 화면의 다른 낱말까지 가려 요약을 망가뜨린다.
		s.fail(w, bad("이름은 2~30자로, 줄바꿈이나 쌍점(:) 없이 적으세요"))
		return
	}
	cfg := s.config()
	for _, t := range cfg.MaskTerms {
		if t == name || t == "이름:"+name {
			writeJSON(w, http.StatusOK, map[string]any{"added": false, "terms": len(cfg.MaskTerms)})
			return
		}
	}
	cfg.MaskTerms = append(slices.Clone(cfg.MaskTerms), "이름:"+name)
	if err := cfg.Validate(); err != nil {
		s.fail(w, bad(err.Error()))
		return
	}
	if err := s.d.SaveConfig(cfg); err != nil {
		s.fail(w, err)
		return
	}
	s.mu.Lock()
	s.cfg.MaskTerms = cfg.MaskTerms
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"added": true, "terms": len(cfg.MaskTerms)})
}

type snapPage struct {
	base
	snapStatus
	DefaultURL  string
	Terms       []string
	NoBrowser   string // Chrome/Edge 를 찾지 못했을 때의 안내
	ProfileHint string
}

func (s *Server) pageSnapshot(w http.ResponseWriter, r *http.Request) {
	p := snapPage{base: s.base(r, "국민신문고 화면 저장"), snapStatus: s.snapStatus(), DefaultURL: snapDefaultURL, Terms: s.config().MaskTerms}
	if s.d.DataDir != "" {
		p.ProfileHint = filepath.Join(s.d.DataDir, "browser-profile")
	}
	if o := s.browserOptions(); o.Bin == "" {
		if _, err := browser.Find(); err != nil {
			p.NoBrowser = err.Error()
		}
	}
	s.render(w, "snapshot", p)
}

func (s *Server) version() string {
	if s.d.Version == "" {
		return "dev"
	}
	return s.d.Version
}
