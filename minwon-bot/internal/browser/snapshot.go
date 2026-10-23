package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/go-rod/rod"

	"minwon-bot/internal/privacy"
)

type SnapshotOptions struct {
	// OutDir 는 저장할 폴더. 이미 있으면 번호를 이어서 쓴다.
	OutDir string
	// Masker 는 가릴 낱말(이름, 부대명). nil 이면 형식 기반 가림만 한다.
	Masker *privacy.Masker
	// Screenshots 가 true 면 화면 그림(PNG)도 저장한다. 화면에 개인정보가 그대로 보이므로 기본은 끈다.
	Screenshots bool
}

type FrameInfo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Note string `json:"note,omitempty"`
}

// PageShot 은 탭 하나를 저장한 결과다.
type PageShot struct {
	Dir      string
	URL      string
	Title    string
	Frames   []FrameInfo
	Outline  string   // 사람이 읽고 대화에 붙여넣을 요약
	Warnings []string // 개인정보처럼 보이는 값이 남았을 가능성 등
}

var skipURL = regexp.MustCompile(`^(about:|chrome:|chrome-extension:|devtools:|edge:)`)

const frameDepth = 3

// Snapshot 은 열려 있는 모든 탭의 화면 구조를 저장한다.
func (s *Session) Snapshot(ctx context.Context, o SnapshotOptions) ([]PageShot, error) {
	if o.Masker == nil {
		o.Masker = privacy.NewMasker(nil)
	}
	if err := os.MkdirAll(o.OutDir, 0o700); err != nil {
		return nil, err
	}
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	seq := nextSeq(o.OutDir)
	var shots []PageShot
	for _, p := range pages {
		if ctx.Err() != nil {
			return shots, ctx.Err()
		}
		info, err := p.Timeout(10 * time.Second).Info()
		if err != nil || skipURL.MatchString(info.URL) || isBlank(info.URL) {
			continue
		}
		shot, err := snapshotPage(ctx, p, o, seq, info.URL, info.Title)
		if err != nil {
			shot.Warnings = append(shot.Warnings, "저장 중 오류: "+err.Error())
		}
		shots = append(shots, shot)
		seq++
	}
	if len(shots) > 0 {
		var all strings.Builder
		for _, sh := range shots {
			fmt.Fprintf(&all, "######## %s\n%s\n\n", filepath.Base(sh.Dir), sh.Outline)
		}
		_ = os.WriteFile(filepath.Join(o.OutDir, fmt.Sprintf("outline-%03d.txt", seq-1)), []byte(all.String()), 0o600)
	}
	return shots, nil
}

func nextSeq(dir string) int {
	entries, _ := os.ReadDir(dir)
	n := 1
	for _, e := range entries {
		if e.IsDir() {
			var k int
			if _, err := fmt.Sscanf(e.Name(), "%d-", &k); err == nil && k >= n {
				n = k + 1
			}
		}
	}
	return n
}

func snapshotPage(ctx context.Context, p *rod.Page, o SnapshotOptions, seq int, rawURL, title string) (PageShot, error) {
	m := o.Masker
	shot := PageShot{URL: sanitizeURL(rawURL), Title: m.Mask(title)}
	shot.Dir = filepath.Join(o.OutDir, fmt.Sprintf("%03d-%s", seq, slug(shot.Title, rawURL)))
	if err := os.MkdirAll(shot.Dir, 0o700); err != nil {
		return shot, err
	}

	var outline strings.Builder
	var written []string // 개인정보 잔여 점검용
	n := 0
	var capture func(fp *rod.Page, name, src string, depth int)
	capture = func(fp *rod.Page, name, src string, depth int) {
		fi := FrameInfo{Name: name, URL: sanitizeURL(src)}
		htmlSrc, err := fp.Timeout(15 * time.Second).HTML()
		if err != nil {
			fi.Note = "HTML 을 읽지 못함: " + err.Error()
			shot.Frames = append(shot.Frames, fi)
			fmt.Fprintf(&outline, "== %s (%s) — %s\n", name, fi.URL, fi.Note)
			return
		}
		clean, err := SanitizeHTML(htmlSrc, m)
		if err != nil {
			fi.Note = "HTML 정리 실패: " + err.Error()
		} else {
			file := "page.html"
			if name != "main" {
				file = name + ".html"
			}
			_ = os.WriteFile(filepath.Join(shot.Dir, file), []byte(clean), 0o600)
			written = append(written, clean)
		}
		lines := frameOutline(fp)
		text := m.Mask(strings.Join(lines, "\n"))
		written = append(written, text)
		fmt.Fprintf(&outline, "== %s (%s)\n%s\n", name, fi.URL, text)
		shot.Frames = append(shot.Frames, fi)

		if depth >= frameDepth {
			return
		}
		els, err := fp.Timeout(5 * time.Second).Elements("iframe, frame")
		if err != nil {
			return
		}
		for _, el := range els {
			if ctx.Err() != nil {
				return
			}
			n++
			child := fmt.Sprintf("frame-%d", n)
			srcAttr, _ := el.Attribute("src")
			src := ""
			if srcAttr != nil {
				src = *srcAttr
			}
			sub, err := el.Frame()
			if err != nil {
				shot.Frames = append(shot.Frames, FrameInfo{Name: child, URL: sanitizeURL(src), Note: "프레임에 접근하지 못함: " + err.Error()})
				fmt.Fprintf(&outline, "== %s (%s) — 접근하지 못함\n", child, sanitizeURL(src))
				continue
			}
			capture(sub, child, src, depth+1)
		}
	}
	capture(p, "main", rawURL, 0)

	shot.Outline = outline.String()
	_ = os.WriteFile(filepath.Join(shot.Dir, "outline.txt"), []byte(shot.Outline), 0o600)
	meta, _ := json.MarshalIndent(map[string]any{
		"saved_at": time.Now().Format(time.RFC3339), "url": shot.URL, "title": shot.Title, "frames": shot.Frames,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(shot.Dir, "meta.json"), meta, 0o600)

	if o.Screenshots {
		if png, err := p.Timeout(15*time.Second).Screenshot(true, nil); err == nil {
			_ = os.WriteFile(filepath.Join(shot.Dir, "page.png"), png, 0o600)
		}
	}
	// 가렸는데도 개인정보 형식이 남았는지 마지막으로 확인한다.
	var left int
	for _, w := range written {
		for _, f := range privacy.Scan(w) {
			left += f.Count
		}
	}
	if left > 0 {
		shot.Warnings = append(shot.Warnings, fmt.Sprintf("개인정보 형식으로 보이는 값이 %d건 남아 있을 수 있습니다. 공유하기 전에 파일을 열어 확인하세요.", left))
	}
	return shot, nil
}

// frameOutline 은 프레임의 화면 요약 줄들이다. 실패하면 이유를 한 줄로 돌려준다.
func frameOutline(p *rod.Page) []string {
	res, err := p.Timeout(10 * time.Second).Eval(outlineJS)
	if err != nil {
		return []string{"(요약을 만들지 못함: " + err.Error() + ")"}
	}
	var lines []string
	for _, v := range res.Value.Arr() {
		lines = append(lines, v.String())
	}
	return lines
}

var nonSlug = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// slug 는 폴더 이름용 짧은 식별자다. 제목이 없으면 주소의 호스트를 쓴다.
func slug(title, rawURL string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		if u, err := url.Parse(rawURL); err == nil {
			s = u.Host
		}
	}
	s = strings.Trim(nonSlug.ReplaceAllString(s, "-"), "-")
	r := []rune(s)
	if len(r) > 30 {
		r = r[:30]
	}
	s = string(r)
	if s == "" || !unicode.IsPrint(r[0]) {
		return "page"
	}
	return s
}
