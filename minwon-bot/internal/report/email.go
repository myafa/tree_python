package report

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Email 은 SMTP 로 보고를 보낸다. starttls 모드는 서버가 암호화를 지원하지 않으면
// 비밀번호를 보내지 않고 실패한다.
type Email struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       string // 쉼표로 여러 명 가능
	Mode     string // starttls | tls | none
	Full     bool

	// TLSConfig 는 테스트에서 자체 서명 인증서를 믿게 할 때만 쓴다.
	TLSConfig *tls.Config
}

func (e *Email) Name() string { return "이메일" }

func (e *Email) tlsConfig() *tls.Config {
	if e.TLSConfig != nil {
		return e.TLSConfig
	}
	return &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
}

func (e *Email) Send(ctx context.Context, r Report) error {
	from, err := mail.ParseAddress(e.From)
	if err != nil {
		return fmt.Errorf("보내는 주소가 올바르지 않습니다: %v", err)
	}
	tos, err := mail.ParseAddressList(e.To)
	if err != nil || len(tos) == 0 {
		return errors.New("받는 주소가 올바르지 않습니다")
	}
	msg := buildMessage(from, tos, "[민원 도우미] "+r.Title, r.External(e.Full), time.Now())

	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if e.Mode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, e.tlsConfig())
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("메일 서버에 연결하지 못했습니다: %v", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(45 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, e.Host)
	if err != nil {
		return fmt.Errorf("메일 서버 응답이 올바르지 않습니다: %v", err)
	}
	defer c.Close()
	if e.Mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("메일 서버가 STARTTLS 를 지원하지 않아 보내지 않았습니다(암호화 없이 비밀번호를 보내지 않기 위해)")
		}
		if err := c.StartTLS(e.tlsConfig()); err != nil {
			return fmt.Errorf("STARTTLS 실패: %v", err)
		}
	}
	if e.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.Username, e.Password, e.Host)); err != nil {
			return fmt.Errorf("메일 서버 로그인 실패: %v", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("보내는 주소를 거절했습니다: %v", err)
	}
	for _, to := range tos {
		if err := c.Rcpt(to.Address); err != nil {
			return fmt.Errorf("받는 주소 %s 를 거절했습니다: %v", to.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("메일 전송 실패: %v", err)
	}
	return c.Quit()
}

// buildMessage 는 UTF-8 본문을 base64 로 담은 RFC 5322 메시지를 만든다.
func buildMessage(from *mail.Address, to []*mail.Address, subject, body string, now time.Time) []byte {
	var tos []string
	for _, t := range to {
		tos = append(tos, t.String())
	}
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", strings.Join(tos, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", now.Format(time.RFC1123Z))
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="utf-8"`)
	h("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}
