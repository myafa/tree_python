package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"

	"minwon-bot/internal/privacy"
)

func TestSanitizeURL(t *testing.T) {
	cases := map[string]string{
		"/minwon/list.do?token=abc&id=77":               "/minwon/list.do?…",
		"https://www.example.kr/a/b;jsessionid=XYZ?x=1": "https://www.example.kr/a/b?…",
		"https://user:pw@host.kr/p#frag":                "https://host.kr/p",
		"/plain/path":                                   "/plain/path",
		"data:image/png;base64,AAAA":                    "data:…",
		"javascript:goPage('1')":                        "javascript:goPage('1')",
		"":                                              "",
	}
	for in, want := range cases {
		if got := sanitizeURL(in); got != want {
			t.Errorf("sanitizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

const dirty = `<!doctype html><html><head><title>민원 신청</title>
<meta name="csrf-token" content="SECRETCSRFVALUE">
<meta name="viewport" content="width=device-width">
<style>.x{color:red} /* 개인 스타일 */</style>
<script nonce="abc123">var token = "SECRET-SCRIPT-TOKEN"; var phone = "010-1111-2222";</script>
<script src="/static/app.js?v=SECRETVERSION"></script>
</head><body>
<!-- 담당자 홍길동 010-3333-4444 -->
<form id="frm" action="/minwon/save.do?sid=SECRETSID" method="post">
  <label for="title">제목</label>
  <input type="text" id="title" name="title" value="홍길동 상병 휴가 반려" placeholder="제목을 입력">
  <input type="hidden" name="_csrf" value="HIDDENSECRETVALUE">
  <textarea id="content" name="content">내 연락처는 010-5555-6666 이고 메일은 me@example.kr 입니다</textarea>
  <select id="ofc"><option value="A01">국방부</option><option value="A02">육군본부</option></select>
  <button type="button" id="save" onclick="doSave('010-7777-8888')">신청</button>
</form>
<p>안녕하세요 홍길동님, 주민번호 900101-1234567</p>
<a href="/minwon/my.do;jsessionid=SESSIONSECRET?page=2" data-token="abcdefghijklmnopqrstuvwxyz0123456789ABCD">나의 민원</a>
<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==">
</body></html>`

func TestSanitizeHTMLRemovesPersonalDataKeepsStructure(t *testing.T) {
	m := privacy.NewMasker([]string{"이름:홍길동"})
	out, err := SanitizeHTML(dirty, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"SECRET-SCRIPT-TOKEN", "SECRETCSRFVALUE", "SECRETVERSION", "SECRETSID", "HIDDENSECRETVALUE", "SESSIONSECRET",
		"010-1111-2222", "010-3333-4444", "010-5555-6666", "010-7777-8888", "me@example.kr", "900101-1234567", "홍길동",
		"abcdefghijklmnopqrstuvwxyz0123456789ABCD", "iVBORw0KGgo", "nonce", "개인 스타일",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("정리한 HTML 에 %q 가 남음:\n%s", secret, out)
		}
	}
	// 화면 구조는 남아야 한다.
	for _, keep := range []string{
		`id="frm"`, `action="/minwon/save.do?…"`, `id="title"`, `name="title"`, `placeholder="제목을 입력"`, `type="hidden"`,
		`name="_csrf"`, `<textarea`, `id="ofc"`, "국방부", "육군본부", `value="A01"`, `id="save"`, "doSave(", "나의 민원",
		`href="/minwon/my.do?…"`, `<script src="/static/app.js?…">`, "<label", "제목",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("구조 정보 %q 가 사라짐:\n%s", keep, out)
		}
	}
	if strings.Contains(out, "<!--") {
		t.Error("주석은 지워야 함")
	}
	// 뷰포트 같은 일반 메타는 유지, 민감 메타는 내용만 제거
	if !strings.Contains(out, `name="viewport"`) || !strings.Contains(out, `content="width=device-width"`) {
		t.Error("일반 meta 는 유지")
	}
}

func TestSlug(t *testing.T) {
	if got := slug("민원 신청 | 국민신문고!", ""); got != "민원-신청-국민신문고" {
		t.Errorf("slug = %q", got)
	}
	if got := slug("", "https://www.epeople.go.kr/x"); got != "www-epeople-go-kr" {
		t.Errorf("제목이 없으면 호스트: %q", got)
	}
	if got := slug("!!!", ""); got != "page" {
		t.Errorf("빈 slug: %q", got)
	}
	if len([]rune(slug(strings.Repeat("가", 100), ""))) != 30 {
		t.Error("30자로 줄여야 함")
	}
}

// ---------------------------------------------------------------- 브라우저 종단 간 (Chromium 이 있을 때만)

func testBrowser(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MINWON_TEST_BROWSER"); p != "" {
		return p
	}
	if m, _ := filepath.Glob("/opt/pw-browsers/chromium-*/chrome-linux/chrome"); len(m) > 0 {
		return m[0]
	}
	if p, err := Find(); err == nil {
		return p
	}
	t.Skip("Chromium/Chrome 이 없어 건너뜀")
	return ""
}

func fakeSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>민원 신청 화면 (홍길동)</title></head><body>
<h1>민원 신청</h1>
<form id="frm" name="frm" action="/save.do?sid=SECRETSID" method="post">
  <table><tr><th>제목</th><td><input type="text" id="title" name="title" value="홍길동 상병 건" maxlength="100" required></td></tr>
  <tr><th>내용</th><td><textarea id="content" name="content" placeholder="내용">연락처 010-2222-3333</textarea></td></tr>
  <tr><th>처리기관</th><td><select id="ofc" name="ofc"><option>선택</option><option value="A01">국방부</option><option value="A02">육군본부</option></select></td></tr></table>
  <input type="hidden" name="_csrf" value="HIDDENSECRETVALUE">
  <label><input type="checkbox" id="pub" name="pub"> 공개</label>
  <button type="button" id="btnSave" onclick="save()">신청</button>
  <input type="submit" value="저장">
</form>
<a href="/my.do;jsessionid=SESSIONSECRET?page=2">나의 민원</a>
<div id="hid" style="display:none"><input type="text" id="secretbox" name="secretbox"></div>
<iframe id="agencyFrame" name="agencyFrame" src="/agency.html"></iframe>
<script>var token="SECRET-SCRIPT-TOKEN"; function save(){}</script>
</body></html>`)
	})
	mux.HandleFunc("/agency.html", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>기관 검색</title></head><body>
<input type="text" id="frameInput" name="q" placeholder="기관명 검색"><button id="frameBtn" type="button">검색</button>
<table id="agencies"><tr><th>기관명</th><th>선택</th></tr><tr><td>국방부</td><td><a href="#" onclick="pick(1)">선택</a></td></tr></table>
<table id="mylist"><tr><th>제목</th><th>상태</th></tr>
<tr onclick="fn_row('2AA-1')"><td>개인민원제목행</td><td>답변완료</td></tr>
<tr><td><a href="/view.do?no=1" onclick="fn_view('2AA-2')">개인민원제목링크</a></td><td>처리중</td></tr></table>
</body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSnapshotEndToEnd(t *testing.T) {
	bin := testBrowser(t)
	site := fakeSite(t)
	sess, err := Launch(Options{ProfileDir: t.TempDir(), Bin: bin, Headless: true, NoSandbox: true})
	if err != nil {
		t.Fatalf("브라우저 실행 실패: %v", err)
	}
	defer sess.Close()
	page, err := sess.Open(site.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if err := page.WaitLoad(); err != nil {
		t.Fatal(err)
	}
	// iframe 이 로드되길 기다린다.
	page.MustElement("#agencyFrame")

	out := t.TempDir()
	m := privacy.NewMasker([]string{"이름:홍길동"})
	shots, err := sess.Snapshot(context.Background(), SnapshotOptions{OutDir: out, Masker: m})
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != 1 {
		t.Fatalf("탭 1개여야 함: %d", len(shots))
	}
	sh := shots[0]
	if strings.Contains(sh.Title, "홍길동") || !strings.Contains(sh.Title, "{{NAME_1}}") {
		t.Errorf("제목의 이름이 가려져야 함: %q", sh.Title)
	}
	if len(sh.Frames) != 2 {
		t.Fatalf("메인 + iframe 1개: %+v", sh.Frames)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(sh.Dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return string(b)
	}
	outline := read("outline.txt")
	for _, want := range []string{
		"[form] id=\"frm\" name=\"frm\" action=/save.do", "[input text] id=\"title\" name=\"title\"", "maxlength=\"100\"",
		"label=\"제목\"", "required", "[textarea] id=\"content\"", "[select] id=\"ofc\"", "options=3", "국방부",
		"[input hidden] name=\"_csrf\"", "[input checkbox] id=\"pub\"", "label=\"공개\"", "[button] id=\"btnSave\"", "text=\"신청\"",
		"onclick=\"save()\"", "[a]", "href=/my.do", "[iframe] id=\"agencyFrame\"", "src=/agency.html",
		"== frame-1", "[input text] id=\"frameInput\"", "placeholder=\"기관명 검색\"", "[table] id=\"agencies\"", "headers=[\"기관명\",\"선택\"]",
		"(숨김)", "(목록 글자 생략)", "onclick=\"fn_view('2AA-2')\"", "href=/view.do", "onclick=\"fn_row('2AA-1')\"",
		"headers=[\"제목\",\"상태\"]",
	} {
		if !strings.Contains(outline, want) {
			t.Errorf("요약에 %q 없음:\n%s", want, outline)
		}
	}
	page1 := read("page.html")
	frame1 := read("frame-1.html")
	for name, content := range map[string]string{"page.html": page1, "frame-1.html": frame1, "outline.txt": outline, "meta.json": read("meta.json")} {
		for _, secret := range []string{"SECRET-SCRIPT-TOKEN", "HIDDENSECRETVALUE", "SESSIONSECRET", "SECRETSID", "010-2222-3333", "홍길동"} {
			if strings.Contains(content, secret) {
				t.Errorf("%s 에 %q 가 남음", name, secret)
			}
		}
		// 표 안 목록 글자(본인 민원 제목)는 요약에 들어가면 안 된다. 정리한 HTML 에는 구조 확인용으로 남는다.
		if name == "outline.txt" {
			for _, listed := range []string{"개인민원제목행", "개인민원제목링크"} {
				if strings.Contains(content, listed) {
					t.Errorf("요약에 목록 글자 %q 가 남음", listed)
				}
			}
		}
	}
	if !strings.Contains(page1, `id="title"`) || !strings.Contains(frame1, `id="frameInput"`) {
		t.Error("정리한 HTML 에 구조가 남아야 함")
	}
	if len(sh.Warnings) != 0 {
		t.Errorf("개인정보 잔여 경고가 없어야 함: %v", sh.Warnings)
	}
	// 파일 권한(개인 폴더)
	if st, err := os.Stat(filepath.Join(sh.Dir, "outline.txt")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("파일 권한 0600: %v %v", st, err)
	}
	// 한 번 더 저장하면 번호가 이어진다.
	again, err := sess.Snapshot(context.Background(), SnapshotOptions{OutDir: out, Masker: m})
	if err != nil || len(again) != 1 || !strings.HasPrefix(filepath.Base(again[0].Dir), "002-") {
		t.Errorf("두 번째 저장은 002 번이어야 함: %+v %v", again, err)
	}
}

func TestLaunchErrorsAreExplained(t *testing.T) {
	if _, err := Launch(Options{ProfileDir: ""}); err == nil {
		t.Error("프로필 폴더가 없으면 오류")
	}
	_, err := Launch(Options{ProfileDir: t.TempDir(), Bin: "/no/such/browser", Headless: true})
	if err == nil || !strings.Contains(err.Error(), "브라우저") {
		t.Errorf("실행 실패 안내: %v", err)
	}
}

// 브라우저를 닫아도 프로필 폴더(로그인 상태)는 남아야 한다. go-rod 의 Cleanup 은 폴더를 지우므로 쓰면 안 된다.
func TestCloseKeepsProfile(t *testing.T) {
	bin := testBrowser(t)
	profile := filepath.Join(t.TempDir(), "profile")
	sess, err := Launch(Options{ProfileDir: profile, Bin: bin, Headless: true, NoSandbox: true})
	if err != nil {
		t.Fatalf("브라우저 실행 실패: %v", err)
	}
	if !sess.Alive() {
		t.Fatal("방금 연 브라우저는 살아 있어야 함")
	}
	sess.Close()
	entries, err := os.ReadDir(profile)
	if err != nil || len(entries) == 0 {
		t.Fatalf("닫은 뒤에도 프로필 폴더가 남아 있어야 함: %v (항목 %d개)", err, len(entries))
	}
	if sess.Alive() {
		t.Error("닫은 뒤에는 Alive 가 false 여야 함")
	}
}

// 연결 주소를 끝내 알려 주지 않는 브라우저(원격 제어를 막는 정책 등)를 무한정 기다리면 화면 저장 버튼이 멈춘다.
func TestLaunchGivesUpWhenBrowserNeverAnswers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("셸 스크립트로 가짜 브라우저를 만든다")
	}
	fake := filepath.Join(t.TempDir(), "fake-browser")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := launchTimeout
	launchTimeout = time.Second
	defer func() { launchTimeout = old }()
	start := time.Now()
	_, err := Launch(Options{ProfileDir: filepath.Join(t.TempDir(), "profile"), Bin: fake})
	if err == nil || !strings.Contains(err.Error(), "준비되지 않았습니다") {
		t.Fatalf("시간 초과 안내가 나와야 함: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("너무 오래 기다림: %v", d)
	}
}

func TestExplainLaunch(t *testing.T) {
	cases := map[string]string{
		"[launcher] Failed to get the debug url: Failed to create /p/SingletonLock: File exists": "이미 열려 있습니다",
		"[launcher] Failed to get the debug url: ":                                               "열리자마자 닫혔습니다",
		"exec: permission denied":                                                                "브라우저를 열지 못했습니다",
		// 프로필 경로(browser-profile)가 들어 있다는 이유만으로 '이미 열려 있음' 으로 보면 안 된다.
		"fork/exec C:/Users/me/minwon-bot/browser-profile/x: access denied": "브라우저를 열지 못했습니다",
	}
	for in, want := range cases {
		if got := explainLaunch(fmt.Errorf("%s", in)).Error(); !strings.Contains(got, want) {
			t.Errorf("%q → %q, %q 가 들어 있어야 함", in, got, want)
		}
	}
}

// 프레임 목록을 찾을 때 건 시간 제한이 뒤쪽 프레임 저장까지 이어지면, 느린 화면에서 뒤쪽 프레임이 빠진다.
func TestLaterFramesGetTheirOwnTime(t *testing.T) {
	bin := testBrowser(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>두 프레임</title></head><body>
<iframe src="/a"></iframe><iframe src="/b"></iframe></body></html>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><body><input type="text" id="first-frame-input"></body></html>`)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><body><input type="text" id="second-frame-input"></body></html>`)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	sess, err := Launch(Options{ProfileDir: filepath.Join(t.TempDir(), "profile"), Bin: bin, Headless: true, NoSandbox: true})
	if err != nil {
		t.Fatalf("브라우저 실행 실패: %v", err)
	}
	defer sess.Close()
	if _, err := sess.Open(site.URL + "/"); err != nil {
		t.Fatal(err)
	}

	oldTimeout, oldHook := frameListTimeout, testHookBeforeFrame
	frameListTimeout = time.Second
	testHookBeforeFrame = func() { time.Sleep(700 * time.Millisecond) } // 두 번째 프레임은 목록을 찾은 지 1초가 지난 뒤
	defer func() { frameListTimeout, testHookBeforeFrame = oldTimeout, oldHook }()

	shots, err := sess.Snapshot(context.Background(), SnapshotOptions{OutDir: t.TempDir()})
	if err != nil || len(shots) != 1 {
		t.Fatalf("저장 실패: %v (%d개)", err, len(shots))
	}
	for _, f := range shots[0].Frames {
		if f.Note != "" {
			t.Errorf("%s: %s", f.Name, f.Note)
		}
	}
	for _, want := range []string{"first-frame-input", "second-frame-input"} {
		if !strings.Contains(shots[0].Outline, want) {
			t.Errorf("요약에 %s 가 없음:\n%s", want, shots[0].Outline)
		}
	}
}

// 사용자가 나중에 연 창(처리기관 선택 창 등)을 '살아 있는지 확인'이 먼저 만나도, 그 확인의 짧은 시간 제한이
// 탭에 남아 나중 저장에서 빠지면 안 된다(rod 는 탭 객체를 처음 만든 문맥 그대로 캐시한다).
func TestTabFirstSeenByChecksIsStillCaptured(t *testing.T) {
	bin := testBrowser(t)
	site := fakeSite(t)
	sess, err := Launch(Options{ProfileDir: filepath.Join(t.TempDir(), "profile"), Bin: bin, Headless: true, NoSandbox: true})
	if err != nil {
		t.Fatalf("브라우저 실행 실패: %v", err)
	}
	defer sess.Close()
	if _, err := sess.Open(site.URL + "/"); err != nil {
		t.Fatal(err)
	}
	// rod 의 탭 객체를 거치지 않고 새 창을 만든다(사이트가 띄운 팝업과 같은 처지).
	if _, err := (proto.TargetCreateTarget{URL: site.URL + "/frame"}).Call(sess.b); err != nil {
		t.Fatal(err)
	}
	old := checkTimeout
	checkTimeout = 300 * time.Millisecond
	defer func() { checkTimeout = old }()
	if !sess.Alive() {
		t.Fatal("살아 있어야 함")
	}
	if err := sess.Show(site.URL + "/"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	shots, err := sess.Snapshot(context.Background(), SnapshotOptions{OutDir: t.TempDir()})
	if err != nil || len(shots) != 2 {
		t.Fatalf("두 탭을 모두 저장해야 함: %v (%d개)", err, len(shots))
	}
}
