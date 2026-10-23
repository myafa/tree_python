package report

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/store"
)

func sample() Report {
	return Report{
		Kind: KindFollowUpSubmitted, CaseID: 12, CaseTitle: "홍길동 상병 휴가 반려",
		Title: "재민원을 제출했습니다", Summary: "2회차 · 접수번호 2AA-2610-0002",
		Body: "재민원 본문: 반려 기준의 조항을 알려 주세요.", At: time.Now(),
	}
}

func TestExternalSummaryHidesContent(t *testing.T) {
	r := sample()
	short := r.External(false)
	for _, secret := range []string{"홍길동", "반려 기준의 조항", "휴가"} {
		if strings.Contains(short, secret) {
			t.Errorf("요약 모드에 내용이 새면 안 됨 (%s):\n%s", secret, short)
		}
	}
	for _, want := range []string{"사안 #12", "재민원을 제출했습니다", "2AA-2610-0002", "보고함"} {
		if !strings.Contains(short, want) {
			t.Errorf("요약에 %q 없음:\n%s", want, short)
		}
	}
	full := r.External(true)
	for _, want := range []string{"홍길동 상병 휴가 반려", "반려 기준의 조항"} {
		if !strings.Contains(full, want) {
			t.Errorf("full 에 %q 없음:\n%s", want, full)
		}
	}
}

func TestInboxAndMulti(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	ctx := context.Background()
	if err := (Inbox{Store: st}).Send(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListReports(ctx, 5)
	if len(list) != 1 || list[0].Title != "재민원을 제출했습니다" ||
		!strings.Contains(list[0].Body, "2AA-2610-0002") || !strings.Contains(list[0].Body, "반려 기준의 조항") {
		t.Fatalf("보고함에는 전체 내용이 저장되어야 함: %+v", list)
	}

	// 한 채널이 실패해도 다른 채널로는 보낸다.
	bad := failing{}
	good := &recorder{}
	err := Multi{bad, good}.Send(ctx, sample())
	if err == nil || !strings.Contains(err.Error(), "고장") || !strings.Contains(err.Error(), "실패하는 채널") {
		t.Fatalf("실패는 채널 이름과 함께 돌려줘야 함: %v", err)
	}
	if len(good.got) != 1 {
		t.Fatal("앞 채널이 실패해도 뒤 채널로 보내야 함")
	}
}

type failing struct{}

func (failing) Name() string                       { return "실패하는 채널" }
func (failing) Send(context.Context, Report) error { return errors.New("고장") }

type recorder struct{ got []Report }

func (r *recorder) Name() string { return "기록" }
func (r *recorder) Send(_ context.Context, x Report) error {
	r.got = append(r.got, x)
	return nil
}

func TestBuildChannelsFollowConfig(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	cfg := config.Default()
	if got := Build(cfg, st).Name(); got != "보고함" {
		t.Fatalf("기본은 보고함만: %s", got)
	}
	cfg.Report.Telegram = config.Telegram{Token: "t", ChatID: "1"}
	cfg.Report.Email = config.Email{Host: "h", Port: 587, From: "a@x.kr", To: "b@x.kr", TLS: "starttls"}
	if got := Build(cfg, st).Name(); got != "보고함, 텔레그램, 이메일" {
		t.Fatalf("설정한 채널이 모두 붙어야 함: %s", got)
	}
}

// ---------------------------------------------------------------- 텔레그램

func TestTelegramSendsSummaryOrFull(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := &Telegram{Token: "TOKEN123", ChatID: "777", BaseURL: srv.URL}
	if err := tg.Send(context.Background(), sample()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/botTOKEN123/sendMessage" || gotBody["chat_id"] != "777" {
		t.Fatalf("요청 불일치: %s %v", gotPath, gotBody)
	}
	text := gotBody["text"].(string)
	if strings.Contains(text, "반려 기준의 조항") || !strings.Contains(text, "2AA-2610-0002") {
		t.Errorf("요약 모드: %s", text)
	}
	tg.Full = true
	tg.Send(context.Background(), sample())
	if !strings.Contains(gotBody["text"].(string), "반려 기준의 조항") {
		t.Error("full 모드에는 본문이 있어야 함")
	}
	// 너무 긴 글은 잘라서 보낸다.
	long := sample()
	long.Body = strings.Repeat("가", 6000)
	tg.Send(context.Background(), long)
	if n := len([]rune(gotBody["text"].(string))); n > 4096 {
		t.Errorf("텔레그램 길이 한도 초과: %d", n)
	}
}

func TestTelegramErrorsNeverLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	tg := &Telegram{Token: "SECRET-TOKEN", ChatID: "1", BaseURL: srv.URL}
	err := tg.Send(context.Background(), sample())
	srv.Close()
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("API 오류는 설명만 보여야 함: %v", err)
	}
	// 연결 실패 오류에도 주소(토큰 포함)가 들어가면 안 된다.
	tg = &Telegram{Token: "SECRET-TOKEN", ChatID: "1", BaseURL: "http://127.0.0.1:1"}
	err = tg.Send(context.Background(), sample())
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("연결 실패 오류에 토큰이 있으면 안 됨: %v", err)
	}
}

// ---------------------------------------------------------------- 이메일 (가짜 SMTP 서버)

type smtpServer struct {
	ln            net.Listener
	tlsCfg        *tls.Config
	implicitTLS   bool
	offerStartTLS bool

	mu         sync.Mutex
	rawData    string
	mailFrom   string
	rcpts      []string
	authLine   string
	sawCmdsTLS []bool // 명령을 받을 때 TLS 위였는지
}

func newSMTPServer(t *testing.T, cfg *tls.Config, implicit, offerStartTLS bool) *smtpServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicit {
		ln = tls.NewListener(ln, cfg)
	}
	s := &smtpServer{ln: ln, tlsCfg: cfg, implicitTLS: implicit, offerStartTLS: offerStartTLS}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *smtpServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *smtpServer) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(l string) { io.WriteString(conn, l+"\r\n") }
	tlsOn := s.implicitTLS
	say("220 localhost ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		s.mu.Lock()
		s.sawCmdsTLS = append(s.sawCmdsTLS, tlsOn)
		s.mu.Unlock()
		switch {
		case strings.HasPrefix(up, "EHLO"):
			say("250-localhost")
			if s.offerStartTLS && !tlsOn {
				say("250-STARTTLS")
			}
			say("250 AUTH PLAIN")
		case up == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(conn, s.tlsCfg)
			if tc.Handshake() != nil {
				return
			}
			conn = tc
			r = bufio.NewReader(tc)
			tlsOn = true
		case strings.HasPrefix(up, "AUTH PLAIN"):
			s.mu.Lock()
			s.authLine = line
			s.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			s.mu.Lock()
			s.mailFrom = line
			s.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			s.mu.Lock()
			s.rcpts = append(s.rcpts, line)
			s.mu.Unlock()
			say("250 ok")
		case up == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.rawData = b.String()
			s.mu.Unlock()
			say("250 queued")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func selfSigned(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{Certificates: []tls.Certificate{cert}}, &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}
}

func decodeMail(t *testing.T, raw string) (subject, from, to, body string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("메일 해석 실패: %v\n%s", err, raw)
	}
	dec := new(mime.WordDecoder)
	subject, _ = dec.DecodeHeader(m.Header.Get("Subject"))
	b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	return subject, m.Header.Get("From"), m.Header.Get("To"), string(b)
}

func emailFor(srv *smtpServer, mode string, client *tls.Config) *Email {
	return &Email{
		Host: "127.0.0.1", Port: srv.port(), Username: "user", Password: "pw",
		From: "민원 도우미 <bot@example.kr>", To: "me@example.kr, other@example.kr", Mode: mode, TLSConfig: client,
	}
}

func TestEmailPlainStartTLSAndImplicitTLS(t *testing.T) {
	serverTLS, clientTLS := selfSigned(t)
	cases := []struct {
		name          string
		mode          string
		implicit      bool
		offerStartTLS bool
		wantTLSCmds   bool
	}{
		{"평문(none)", "none", false, false, false},
		{"STARTTLS", "starttls", false, true, true},
		{"암묵적 TLS", "tls", true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSMTPServer(t, serverTLS, tc.implicit, tc.offerStartTLS)
			e := emailFor(srv, tc.mode, clientTLS)
			if err := e.Send(context.Background(), sample()); err != nil {
				t.Fatalf("전송 실패: %v", err)
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()
			subject, from, to, body := decodeMail(t, srv.rawData)
			if subject != "[민원 도우미] 재민원을 제출했습니다" {
				t.Errorf("제목(UTF-8 인코딩) 불일치: %q", subject)
			}
			if !strings.Contains(from, "bot@example.kr") || !strings.Contains(to, "me@example.kr") || !strings.Contains(to, "other@example.kr") {
				t.Errorf("주소 불일치: from=%q to=%q", from, to)
			}
			if !strings.Contains(body, "2AA-2610-0002") || strings.Contains(body, "반려 기준의 조항") {
				t.Errorf("요약 모드 본문이어야 함:\n%s", body)
			}
			if len(srv.rcpts) != 2 {
				t.Errorf("수신자 2명이어야 함: %v", srv.rcpts)
			}
			if !strings.HasPrefix(srv.authLine, "AUTH PLAIN ") {
				t.Errorf("로그인 시도가 있어야 함: %q", srv.authLine)
			}
			// 암호화 모드에서는 AUTH 이후 모든 명령이 TLS 위에서 와야 한다.
			if tc.wantTLSCmds {
				last := srv.sawCmdsTLS[len(srv.sawCmdsTLS)-1]
				if !last {
					t.Error("마지막 명령이 암호화되지 않은 연결로 왔음")
				}
			}
		})
	}
}

func TestEmailStartTLSRefusesToSendCredentialsInClear(t *testing.T) {
	serverTLS, clientTLS := selfSigned(t)
	srv := newSMTPServer(t, serverTLS, false, false) // STARTTLS 를 알리지 않는 서버
	e := emailFor(srv, "starttls", clientTLS)
	err := e.Send(context.Background(), sample())
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("STARTTLS 가 없으면 실패해야 함: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.authLine != "" || srv.rawData != "" {
		t.Fatalf("암호화 없이 비밀번호나 메일을 보내면 안 됨: auth=%q", srv.authLine)
	}
}

func TestEmailErrors(t *testing.T) {
	e := &Email{Host: "127.0.0.1", Port: 1, From: "not an address", To: "a@x.kr", Mode: "none"}
	if err := e.Send(context.Background(), sample()); err == nil || !strings.Contains(err.Error(), "보내는 주소") {
		t.Errorf("보내는 주소 오류: %v", err)
	}
	e = &Email{Host: "127.0.0.1", Port: 1, From: "a@x.kr", To: "", Mode: "none"}
	if err := e.Send(context.Background(), sample()); err == nil || !strings.Contains(err.Error(), "받는 주소") {
		t.Errorf("받는 주소 오류: %v", err)
	}
	e = &Email{Host: "127.0.0.1", Port: 1, From: "a@x.kr", To: "b@x.kr", Mode: "none"}
	if err := e.Send(context.Background(), sample()); err == nil || !strings.Contains(err.Error(), "연결하지 못했습니다") {
		t.Errorf("연결 오류: %v", err)
	}
	_ = strconv.Itoa
}
