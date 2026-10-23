// minwon-bot 은 민원 초안 작성, 답변 평가, 재민원 작성을 돕는 로컬 도구다.
// 이 PC 의 127.0.0.1 에서만 열리는 웹 화면으로 쓴다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"minwon-bot/internal/config"
	"minwon-bot/internal/store"
	"minwon-bot/internal/web"
)

const defaultPort = 8745

func main() {
	port := flag.Int("port", defaultPort, "화면을 열 포트(사용 중이면 빈 포트를 고릅니다)")
	noBrowser := flag.Bool("no-browser", false, "브라우저를 자동으로 열지 않습니다")
	flag.Parse()

	if err := run(*port, !*noBrowser); err != nil {
		fmt.Fprintln(os.Stderr, "오류:", err)
		os.Exit(1)
	}
}

func run(port int, openBrowser bool) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// 이미 있던 폴더도 다른 사용자가 못 읽게 한다(Windows 에서는 효과 없음, 사용자 폴더 권한을 따른다).
	_ = os.Chmod(dir, 0o700)
	cfg, err := config.Load(dir)
	if err != nil {
		return fmt.Errorf("설정을 읽지 못했습니다(%s): %w", filepath.Join(dir, "config.json"), err)
	}
	dbPath := filepath.Join(dir, "minwon.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("데이터베이스를 열지 못했습니다: %w", err)
	}
	defer st.Close()
	// 사안 내용과 답변이 들어 있으므로 다른 사용자가 읽지 못하게 한다.
	_ = os.Chmod(dbPath, 0o600)

	srv, err := web.New(web.Deps{
		Store:      st,
		Config:     cfg,
		SaveConfig: func(c config.Config) error { return config.Save(dir, c) },
	})
	if err != nil {
		return err
	}

	ln, err := listen(port)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	fmt.Printf("민원 도우미가 열렸습니다: %s\n", url)
	fmt.Printf("데이터 폴더: %s\n", dir)
	fmt.Println("끝내려면 이 창에서 Ctrl+C 를 누르세요.")
	if openBrowser {
		if err := open(url); err != nil {
			fmt.Println("브라우저를 자동으로 열지 못했습니다. 위 주소를 직접 여세요.")
		}
	}

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			log.Printf("종료 중 오류: %v", err)
		}
	}
	return nil
}

// listen 은 127.0.0.1 에서만 받는다. 정한 포트가 사용 중이면 빈 포트를 쓴다.
func listen(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		return ln, nil
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func open(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
