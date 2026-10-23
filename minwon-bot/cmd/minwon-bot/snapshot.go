package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"minwon-bot/internal/browser"
	"minwon-bot/internal/config"
	"minwon-bot/internal/privacy"
)

const snapshotHelp = `
브라우저가 열렸습니다. 이 브라우저에서 직접 로그인하세요(간편인증, 공동인증서 등).
이 도구는 로그인을 대신하지 않고, 비밀번호나 인증 정보를 읽지 않습니다.

다음 화면들을 차례로 열고, 화면마다 이 터미널에서 Enter 를 누르면 열려 있는 모든 탭을 저장합니다.
  1) 로그인 전 첫 화면과 로그인 화면
  2) 로그인 직후 화면
  3) 민원 신청(작성) 화면 — 처리기관 검색·선택 창이 따로 뜨면 그 창도 열어 둔 채로
  4) '나의 민원' 목록 화면
  5) 답변이 달린 민원의 상세(답변) 화면
끝내려면 q 를 입력하고 Enter 를 누르세요.

저장하는 것: 화면 구조(HTML)와 입력칸·버튼·링크 요약.
저장하지 않는 것: 입력한 값, 스크립트·스타일 내용, 쿠키, 주소의 쿼리와 세션 정보.
주민번호·전화번호·이메일·계좌번호 형식과 설정한 이름·부대명은 자리표시자로 바꿉니다.
`

func runSnapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	url := fs.String("url", "https://www.epeople.go.kr/", "처음 열 주소")
	out := fs.String("out", "", "저장할 폴더(기본: 데이터 폴더/snapshots/날짜-시각)")
	shots := fs.Bool("screenshots", false, "화면 그림(PNG)도 저장합니다. 화면에 개인정보가 그대로 보이므로 직접 확인한 뒤 공유하세요")
	mask := fs.String("mask", "", "추가로 가릴 낱말, 쉼표로 구분(예: 이름:홍길동,부대:제7사단). 설정의 가릴 낱말은 자동으로 적용됩니다")
	bin := fs.String("browser", "", "Chrome/Edge 실행 파일 경로(비우면 자동으로 찾음)")
	headless := fs.Bool("headless", false, "창 없이 실행합니다. 로그인할 수 없으므로 시험용입니다")
	noSandbox := fs.Bool("no-sandbox", false, "브라우저 샌드박스를 끕니다. root 로 실행하는 컨테이너에서만 쓰세요")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir, err := config.Dir()
	if err != nil {
		return err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		cfg = config.Default() // 설정이 깨져 있어도 스냅샷은 뜰 수 있게 한다
	}
	terms := append([]string{}, cfg.MaskTerms...)
	for _, t := range strings.Split(*mask, ",") {
		if t = strings.TrimSpace(t); t != "" {
			terms = append(terms, t)
		}
	}
	outDir := *out
	if outDir == "" {
		outDir = filepath.Join(dir, "snapshots", time.Now().Format("20060102-150405"))
	}

	sess, err := browser.Launch(browser.Options{
		ProfileDir: filepath.Join(dir, "browser-profile"), Bin: *bin, Headless: *headless, NoSandbox: *noSandbox,
	})
	if err != nil {
		return err
	}
	defer sess.Close()
	if _, err := sess.Open(*url); err != nil {
		return fmt.Errorf("주소를 열지 못했습니다: %w", err)
	}
	fmt.Print(snapshotHelp)
	fmt.Printf("저장 폴더: %s\n\n", outDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	masker := privacy.NewMasker(terms)
	in := bufio.NewScanner(os.Stdin)
	saved := 0
	for {
		fmt.Print("Enter = 지금 화면 저장, q = 끝내기 > ")
		if !in.Scan() {
			break
		}
		if l := strings.ToLower(strings.TrimSpace(in.Text())); l == "q" || l == "quit" || l == "exit" {
			break
		}
		res, err := sess.Snapshot(ctx, browser.SnapshotOptions{OutDir: outDir, Masker: masker, Screenshots: *shots})
		if err != nil {
			fmt.Println("저장하지 못했습니다:", err)
			continue
		}
		if len(res) == 0 {
			fmt.Println("저장할 탭이 없습니다. 브라우저에서 화면을 연 뒤 다시 Enter 를 누르세요.")
			continue
		}
		for _, r := range res {
			saved++
			fmt.Printf("  저장: %s  (제목: %s, 프레임 %d개)\n", filepath.Base(r.Dir), r.Title, len(r.Frames))
			for _, w := range r.Warnings {
				fmt.Println("  ⚠", w)
			}
		}
	}
	if saved == 0 {
		fmt.Println("저장한 화면이 없습니다.")
		return nil
	}
	fmt.Printf("\n끝났습니다. %d개 화면을 저장했습니다: %s\n", saved, outDir)
	fmt.Println("각 화면 폴더의 outline.txt 가 가장 중요합니다(길이가 짧고 구조만 담겨 있습니다).")
	fmt.Println("공유하기 전에 파일을 열어 개인정보가 남아 있지 않은지 직접 확인하세요.")
	return nil
}
