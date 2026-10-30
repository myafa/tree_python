package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minwon-bot/internal/browser"
	"minwon-bot/internal/config"
)

func testBrowserBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MINWON_TEST_BROWSER"); p != "" {
		return p
	}
	if m, _ := filepath.Glob("/opt/pw-browsers/chromium-*/chrome-linux/chrome"); len(m) > 0 {
		return m[0]
	}
	if p, err := browser.Find(); err == nil {
		return p
	}
	t.Skip("Chromium/Chrome 이 없어 건너뜀")
	return ""
}

// withSnapshot 은 화면 저장 기능을 창 없는 브라우저로 쓰게 한다.
func withSnapshot(dataDir, bin string) envOpt {
	return func(_ *config.Config, d *Deps) {
		d.DataDir = dataDir
		d.Browser = func() browser.Options { return browser.Options{Bin: bin, Headless: true, NoSandbox: true} }
		d.Version = "test-ver"
	}
}

func snapSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>나의 민원 (홍길동)</title></head><body>
<div id="hd">홍길동님 환영합니다 <a href="/logout.do">로그아웃</a></div>
<form id="frm" action="/save.do?sid=SECRETSID" method="post">
  <input type="text" id="title" name="title" value="홍길동 개인 값">
  <textarea id="content" name="content">연락처 010-2222-3333</textarea>
  <input type="hidden" name="_csrf" value="SECRETCSRF">
  <button type="button" id="go" onclick="fn_save()">신청</button>
</form>
<table id="mylist"><tr><th>제목</th><th>상태</th></tr>
<tr><td><a href="/view.do?no=1" onclick="fn_view('2AA-2')">내 개인 민원 제목</a></td><td>답변완료</td></tr></table>
<iframe src="/frame"></iframe>
</body></html>`)
	})
	mux.HandleFunc("/frame", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>기관 검색</title></head>
<body><input type="text" id="q" name="q" placeholder="기관명"><button id="find" type="button">검색</button></body></html>`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// snapStatusOf 는 응답에서 화면 저장 상태를 꺼낸다(start 는 status 안에, 나머지는 바로 담겨 온다).
func snapStatusOf(m map[string]any) map[string]any {
	if st, ok := m["status"].(map[string]any); ok {
		return st
	}
	return m
}

func TestSnapshotGuards(t *testing.T) {
	e := newEnv(t)

	code, body := e.get("/snapshot")
	if code != 200 || !strings.Contains(body, "가릴 이름이 없습니다") || !strings.Contains(body, `href="/snapshot"`) {
		t.Fatalf("화면 저장 페이지: %d", code)
	}
	if strings.Contains(body, ` style="`) || strings.Contains(body, "<script>") {
		t.Error("인라인 스타일·스크립트가 있으면 안 됨")
	}
	if _, b := e.get("/settings"); !strings.Contains(b, `href="/snapshot"`) {
		t.Error("설정 화면에서 화면 저장으로 가는 링크가 있어야 함")
	}

	if code, m := e.post("/api/snapshot/capture", nil); code != http.StatusConflict || !strings.Contains(m["error"].(string), "브라우저 열기") {
		t.Errorf("열기 전 저장은 409 와 안내: %d %v", code, m)
	}
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://example.com/", "http://"} {
		if code, _ := e.post("/api/snapshot/start", map[string]any{"url": u}); code != http.StatusBadRequest {
			t.Errorf("%s 는 거절해야 함: %d", u, code)
		}
	}
	if code, m := e.post("/api/snapshot/start", map[string]any{}); code != http.StatusConflict || !strings.Contains(m["error"].(string), "데이터 폴더") {
		t.Errorf("데이터 폴더가 없으면 409: %d %v", code, m)
	}
	resp, _ := e.do("POST", "/api/snapshot/start", map[string]any{}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF 토큰 없이 브라우저를 띄우면 안 됨: %d", resp.StatusCode)
	}

	// 가릴 이름: 한 글자나 종류 표시가 섞인 값은 거절, 같은 이름은 한 번만 저장
	for _, bad := range []string{"홍", "이름:홍길동", "홍길동\n김철수", ""} {
		if code, _ := e.post("/api/snapshot/mask", map[string]any{"name": bad}); code != http.StatusBadRequest {
			t.Errorf("%q 는 거절: %d", bad, code)
		}
	}
	if m := e.mustOK("/api/snapshot/mask", map[string]any{"name": " 홍길동 "}); m["added"] != true {
		t.Errorf("추가되어야 함: %v", m)
	}
	if m := e.mustOK("/api/snapshot/mask", map[string]any{"name": "홍길동"}); m["added"] != false {
		t.Errorf("이미 있는 이름은 다시 넣지 않음: %v", m)
	}
	if len(e.saved) != 1 || strings.Join(e.saved[0].MaskTerms, ",") != "이름:홍길동" {
		t.Fatalf("설정에 저장되어야 함: %+v", e.saved)
	}
	if got := e.srv.config().MaskTerms; len(got) != 1 {
		t.Errorf("서버 설정에도 반영: %v", got)
	}
	if _, b := e.get("/snapshot"); strings.Contains(b, "가릴 이름이 없습니다") || !strings.Contains(b, "이름:홍길동") {
		t.Error("페이지에 지금 가리는 낱말이 보여야 함")
	}
}

func TestSnapshotFlowWithBrowser(t *testing.T) {
	bin := testBrowserBin(t)
	dataDir := t.TempDir()
	site := snapSite(t)
	e := newEnv(t, withSnapshot(dataDir, bin))
	t.Cleanup(e.srv.Close)

	e.mustOK("/api/snapshot/mask", map[string]any{"name": "홍길동"})
	st := snapStatusOf(e.mustOK("/api/snapshot/start", map[string]any{"url": site.URL + "/"}))
	if st["open"] != true {
		t.Fatalf("열린 상태여야 함: %v", st)
	}

	m := e.mustOK("/api/snapshot/capture", nil)
	if m["saved"] != float64(1) {
		t.Fatalf("탭 하나를 저장해야 함: %v", m)
	}
	text := m["text"].(string)
	for _, want := range []string{"# 민원 도우미 화면 구조 요약 (버전 test-ver", "######## 001-", "[form]", "== frame-1", "(목록 글자 생략)", `onclick="fn_view('2AA-2')"`, "{{NAME_1}}"} {
		if !strings.Contains(text, want) {
			t.Errorf("모은 글에 %q 가 없음:\n%s", want, text)
		}
	}
	for _, secret := range []string{"홍길동", "010-2222-3333", "SECRETSID", "SECRETCSRF", "내 개인 민원 제목"} {
		if strings.Contains(text, secret) {
			t.Errorf("모은 글에 %q 가 남음", secret)
		}
	}
	dir := m["dir"].(string)
	if !strings.HasPrefix(dir, filepath.Join(dataDir, "snapshots")) {
		t.Errorf("저장 폴더는 데이터 폴더 아래: %s", dir)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "001-*", "outline.txt")); len(files) != 1 {
		t.Errorf("outline.txt 가 저장되어야 함: %v", files)
	}

	// 다시 누르면 새로 띄우지 않고 열린 브라우저를 쓴다.
	if m := e.mustOK("/api/snapshot/start", map[string]any{}); m["reused"] != true {
		t.Errorf("열린 브라우저를 다시 써야 함: %v", m)
	}
	if code, b := e.get("/snapshot"); code != 200 || !strings.Contains(b, "저장용 브라우저가 열려 있습니다") || !strings.Contains(b, "저장한 화면 1개") {
		t.Errorf("페이지가 지금 상태를 보여야 함: %d", code)
	}

	// 사용자가 창을 직접 닫은 경우: 안내 오류를 내고, 모은 글은 남고, 다시 열면 새로 띄운다.
	e.srv.snap.sess.Close()
	code, em := e.post("/api/snapshot/capture", nil)
	if code != http.StatusConflict || !strings.Contains(em["error"].(string), "닫혀 있습니다") {
		t.Fatalf("닫힌 브라우저 안내: %d %v", code, em)
	}
	_, raw := e.get("/api/snapshot/status")
	if !strings.Contains(raw, `"open":false`) || !strings.Contains(raw, "######## 001-") {
		t.Errorf("닫힌 상태와 모은 글이 남아 있어야 함: %s", raw)
	}
	if m := e.mustOK("/api/snapshot/start", map[string]any{"url": site.URL + "/"}); m["reused"] != false {
		t.Errorf("새로 띄워야 함: %v", m)
	}
	m = e.mustOK("/api/snapshot/capture", nil)
	if shots := m["shots"].([]any); len(shots) != 2 || !strings.HasPrefix(shots[1].(map[string]any)["name"].(string), "002-") {
		t.Errorf("같은 폴더에 이어서 쌓여야 함: %v", m["shots"])
	}
	if m["dir"] != dir {
		t.Errorf("저장 폴더가 바뀌면 안 됨: %v != %s", m["dir"], dir)
	}

	if m := e.mustOK("/api/snapshot/stop", nil); m["open"] != false {
		t.Errorf("닫힌 상태여야 함: %v", m)
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "browser-profile")); err != nil || len(entries) == 0 {
		t.Errorf("닫은 뒤에도 로그인 상태가 든 프로필이 남아야 함: %v", err)
	}
	if code, _ := e.post("/api/snapshot/capture", nil); code != http.StatusConflict {
		t.Errorf("닫은 뒤 저장은 409: %d", code)
	}

	// 주소를 열지 못해도(접속 거부) 띄운 브라우저는 닫지 않는다.
	code, em = e.post("/api/snapshot/start", map[string]any{"url": "http://127.0.0.1:1/"})
	if code != http.StatusBadGateway || !strings.Contains(em["error"].(string), "브라우저는 열었지만") {
		t.Errorf("주소 실패 안내: %d %v", code, em)
	}
	if _, raw := e.get("/api/snapshot/status"); !strings.Contains(raw, `"open":true`) {
		t.Errorf("브라우저는 열린 채로 남아야 함: %s", raw)
	}
	e.mustOK("/api/snapshot/stop", nil)

	m = e.mustOK("/api/snapshot/reset", nil)
	if m["text"] != "" || len(m["shots"].([]any)) != 0 || m["dir"] != nil {
		t.Errorf("새로 시작하면 목록이 비어야 함: %v", m)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*", "outline.txt")); len(files) != 2 {
		t.Errorf("새로 시작해도 저장한 파일은 남아야 함: %v", files)
	}
}
