// Package web 은 127.0.0.1 에서만 열리는 로컬 화면과 JSON API 를 제공한다.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/llm"
	"minwon-bot/internal/minwon"
	"minwon-bot/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Deps struct {
	Store      *store.Store
	Config     config.Config
	SaveConfig func(config.Config) error
	// NewLLM 은 설정으로 AI 백엔드를 만든다. 비우면 DefaultLLM.
	NewLLM func(config.Config) llm.LLM
	Now    func() time.Time
}

// DefaultLLM 은 설정에 맞는 백엔드를 만든다.
func DefaultLLM(c config.Config) llm.LLM {
	timeout := time.Duration(c.LLM.TimeoutSec) * time.Second
	if c.LLM.Backend == config.BackendAPI {
		return llm.NewAPI(c.LLM.Model, c.LLM.Effort, timeout)
	}
	return llm.NewClaudeCode(c.LLM.ClaudeBin, c.LLM.Model, timeout)
}

type Server struct {
	d     Deps
	csrf  string
	pages map[string]*template.Template

	mu  sync.RWMutex
	cfg config.Config
	llm llm.LLM

	// op 는 AI 를 부르며 상태를 바꾸는 작업을 한 번에 하나만 하게 한다.
	op sync.Mutex
}

func New(d Deps) (*Server, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewLLM == nil {
		d.NewLLM = DefaultLLM
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	s := &Server{d: d, csrf: hex.EncodeToString(tok), cfg: d.Config, llm: d.NewLLM(d.Config), pages: map[string]*template.Template{}}
	for _, name := range []string{"cases", "case_new", "case", "settings"} {
		t, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("템플릿 %s: %w", name, err)
		}
		s.pages[name] = t
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.pageCases)
	mux.HandleFunc("GET /cases/new", s.pageCaseNew)
	mux.HandleFunc("POST /cases", s.postCase)
	mux.HandleFunc("GET /cases/{id}", s.pageCase)
	mux.HandleFunc("GET /settings", s.pageSettings)
	mux.Handle("GET /static/", http.FileServerFS(assets))

	mux.HandleFunc("POST /api/cases/{id}/draft", s.apiDraft)
	mux.HandleFunc("POST /api/cases/{id}/close", s.apiClose)
	mux.HandleFunc("POST /api/submissions/{id}/save", s.apiSave)
	mux.HandleFunc("POST /api/submissions/{id}/submitted", s.apiSubmitted)
	mux.HandleFunc("POST /api/submissions/{id}/discard", s.apiDiscard)
	mux.HandleFunc("POST /api/submissions/{id}/answers", s.apiAnswerAdd)
	mux.HandleFunc("POST /api/answers/{id}/evaluate", s.apiEvaluate)
	mux.HandleFunc("POST /api/answers/{id}/followup", s.apiFollowUp)
	mux.HandleFunc("POST /api/settings", s.apiSettings)
	mux.HandleFunc("POST /api/llm/test", s.apiLLMTest)
	mux.HandleFunc("POST /api/style/samples", s.apiStyleAdd)
	mux.HandleFunc("POST /api/style/samples/{id}/delete", s.apiStyleDelete)
	mux.HandleFunc("POST /api/style/extract", s.apiStyleExtract)
	mux.HandleFunc("POST /api/style/reset", s.apiStyleReset)
	return s.secure(mux)
}

// ---------------------------------------------------------------- 보안

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		// 다른 사이트가 DNS 를 127.0.0.1 로 돌려 이 서버를 부르는 공격(DNS rebinding)을 막는다.
		if !loopbackHost(r.Host) {
			http.Error(w, "허용되지 않은 접근입니다", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !s.csrfOK(r) {
				http.Error(w, "요청을 확인하지 못했습니다. 페이지를 새로 고친 뒤 다시 시도하세요", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *Server) csrfOK(r *http.Request) bool {
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
		if err := r.ParseForm(); err == nil {
			tok = r.PostForm.Get("csrf")
		}
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.csrf)) == 1
}

// ---------------------------------------------------------------- 도우미

func (s *Server) config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Server) svc() *minwon.Service {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return minwon.New(s.llm, s.cfg.MaskTerms)
}

func (s *Server) backendName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.llm.Name()
}

func (s *Server) loadStyle(ctx context.Context) *minwon.StyleProfile {
	v, err := s.d.Store.GetSetting(ctx, styleKey)
	if err != nil {
		return nil
	}
	p, _ := minwon.ParseStyle(v)
	return p
}

const styleKey = "style_profile"

type base struct {
	Title, CSRF, Backend string
}

func (s *Server) base(title string) base {
	return base{Title: title, CSRF: s.csrf, Backend: s.backendName()}
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("화면 %s 그리기 실패: %v", page, err)
		http.Error(w, "화면을 그리지 못했습니다", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// userErr 는 사용자에게 그대로 보여 줄 수 있는 오류다.
type userErr struct {
	code int
	msg  string
}

func (e *userErr) Error() string { return e.msg }

func bad(msg string) error      { return &userErr{http.StatusBadRequest, msg} }
func conflict(msg string) error { return &userErr{http.StatusConflict, msg} }

func (s *Server) fail(w http.ResponseWriter, err error) {
	var ue *userErr
	status, msg := http.StatusBadGateway, err.Error()
	switch {
	case errors.As(err, &ue):
		status, msg = ue.code, ue.msg
	case errors.Is(err, store.ErrNotFound):
		status, msg = http.StatusNotFound, "찾을 수 없습니다. 이미 지워졌거나 제출 후 바뀌었을 수 있습니다"
	case errors.Is(err, llm.ErrRefusal):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, minwon.ErrNothingToFollowUp):
		status = http.StatusConflict
	case errors.Is(err, minwon.ErrNoAsks), errors.Is(err, minwon.ErrEmptyAnswer):
		status = http.StatusBadRequest
	case errors.Is(err, context.Canceled):
		return // 사용자가 창을 닫음
	}
	if status >= 500 {
		log.Printf("API 오류(%d): %v", status, err)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return bad("요청 형식이 올바르지 않습니다")
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, bad("잘못된 번호입니다")
	}
	return id, nil
}

var funcs = template.FuncMap{
	"fdate": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	"join": strings.Join,
	"excerpt": func(s string, n int) string {
		// 줄바꿈과 겹친 공백을 한 칸으로 줄여 목록에서 한 줄 요약으로 보이게 한다.
		r := []rune(strings.Join(strings.Fields(s), " "))
		if len(r) <= n {
			return string(r)
		}
		return string(r[:n]) + "…"
	},
	"verdictLabel": func(v string) string {
		return map[string]string{
			minwon.VerdictSufficient: "충분", minwon.VerdictPartial: "일부만",
			minwon.VerdictEvasive: "회피", minwon.VerdictUnanswered: "무응답", minwon.VerdictUndetermined: "판정 불가",
		}[v]
	},
	"confLabel": func(v string) string {
		return map[string]string{"high": "높음", "medium": "보통", "low": "낮음"}[v]
	},
	"recLabel": func(v string) string {
		return map[string]string{
			minwon.RecClose: "종결 권고", minwon.RecFollowUp: "재민원 권고",
			minwon.RecOtherRoute: "다른 경로 권고", minwon.RecHold: "판단 보류",
		}[v]
	},
	"eventLabel": func(v string) string {
		if l, ok := map[string]string{
			"draft_created": "초안 작성", "followup_created": "재민원 초안 작성", "submitted": "제출 기록",
			"answer_added": "답변 기록", "evaluated": "답변 평가", "closed": "사안 종결", "discarded": "초안 버림",
		}[v]; ok {
			return l
		}
		return v
	},
}
