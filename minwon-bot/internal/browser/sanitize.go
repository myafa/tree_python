package browser

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"minwon-bot/internal/privacy"
)

// SanitizeHTML 은 화면 구조는 남기고 개인 데이터는 지운 HTML 을 돌려준다.
//
//   - 입력칸의 값, textarea 내용, 스크립트·스타일의 내용, 주석을 지운다.
//   - 주소(href, src, action 등)의 쿼리·조각·세션 매개변수를 지운다.
//   - 토큰처럼 보이는 긴 속성값과 아주 긴 값(data: 이미지 등)은 줄인다.
//   - 본문과 속성의 주민번호·전화·이메일·계좌 형식과 지정한 낱말은 자리표시자로 바꾼다.
func SanitizeHTML(src string, m *privacy.Masker) (string, error) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", err
	}
	if m == nil {
		m = privacy.NewMasker(nil)
	}
	sanitizeNode(doc, m)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return "", err
	}
	return buf.String(), nil
}

var urlAttrs = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true, "data": true,
	"poster": true, "cite": true, "data-url": true, "data-href": true, "data-src": true,
}

var tokenLike = regexp.MustCompile(`^[A-Za-z0-9+/=_\-.]{32,}$`)
var sensitiveMeta = regexp.MustCompile(`(?i)csrf|token|session|nonce`)

const maxAttr = 600

func sanitizeNode(n *html.Node, m *privacy.Masker) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch c.Type {
		case html.CommentNode:
			n.RemoveChild(c)
		case html.TextNode:
			if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "textarea") {
				n.RemoveChild(c)
			} else {
				c.Data = m.Mask(c.Data)
			}
		case html.ElementNode:
			sanitizeElement(c, m)
			sanitizeNode(c, m)
		}
		c = next
	}
}

func sanitizeElement(e *html.Node, m *privacy.Masker) {
	isScript := e.Data == "script"
	var keep []html.Attribute
	var metaName string
	for _, a := range e.Attr {
		if e.Data == "meta" && a.Key == "name" {
			metaName = a.Val
		}
	}
	for _, a := range e.Attr {
		key := strings.ToLower(a.Key)
		val := a.Val
		switch {
		case key == "nonce" || key == "integrity":
			continue
		case isScript && key != "src" && key != "type":
			continue
		case e.Data == "input" && key == "value":
			continue
		case e.Data == "meta" && key == "content" && sensitiveMeta.MatchString(metaName+" "+val):
			continue
		case urlAttrs[key]:
			val = sanitizeURL(val)
		}
		if tokenLike.MatchString(val) && !urlAttrs[key] && key != "id" && key != "name" && key != "class" {
			val = "⟨토큰 생략⟩"
		}
		if len(val) > maxAttr {
			val = val[:maxAttr] + "…(생략)"
		}
		a.Val = m.Mask(val)
		keep = append(keep, a)
	}
	e.Attr = keep
}

// sanitizeURL 은 주소에서 쿼리, 조각, 세션 매개변수(;jsessionid=...)를 지운다.
func sanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") {
		if strings.HasPrefix(raw, "data:") {
			return "data:…"
		}
		return raw
	}
	if strings.HasPrefix(strings.ToLower(raw), "javascript:") {
		return raw // 버튼 동작을 알아보는 데 필요하다. 값은 아래 Mask 에서 가려진다.
	}
	u, err := url.Parse(raw)
	if err != nil {
		return cutURL(raw)
	}
	u.RawQuery, u.Fragment, u.RawFragment, u.User = "", "", "", nil
	if i := strings.Index(u.Path, ";"); i >= 0 {
		u.Path = u.Path[:i]
	}
	s := u.String()
	if strings.Contains(raw, "?") {
		s += "?…"
	}
	return s
}

func cutURL(raw string) string {
	if i := strings.IndexAny(raw, "?#;"); i >= 0 {
		return raw[:i] + "?…"
	}
	return raw
}
