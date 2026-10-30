**민원 도우미 시험판** — 커밋 `{{VERSION}}` (브랜치 `{{BRANCH}}`, {{DATE}} 빌드)

민원 작성·답변 평가·재민원 작성과 답변 후 자동 처리를 돕는 개인용 도구입니다. 법률 자문이 아니며, 제출 전 내용은 직접 확인하세요.

### 내 PC 에 맞는 파일
| PC | 파일 |
|---|---|
| Windows | `minwon-bot-windows-amd64.zip` |
| Mac (Apple Silicon, M1 이후) | `minwon-bot-darwin-arm64.tar.gz` |
| Mac (Intel) | `minwon-bot-darwin-amd64.tar.gz` |
| Linux | `minwon-bot-linux-amd64.tar.gz` |

Mac 종류는 터미널에서 `uname -m` 으로 확인합니다(`arm64` 또는 `x86_64`).

### 실행
- **먼저** Claude Code 를 설치하고 로그인하세요(구독 방식, 추가 크레딧 없음). API 키 방식도 고를 수 있습니다(호출마다 과금).
- **Windows**: 압축을 풀고 `minwon-bot.exe` 더블클릭. "Windows 의 PC 보호"가 뜨면 **추가 정보 → 실행**.
- **macOS**: 압축을 풀고 터미널에서 `./minwon-bot`. "확인되지 않은 개발자"가 뜨면 **시스템 설정 → 개인정보 보호 및 보안 → 확인 없이 열기**, 또는 `xattr -d com.apple.quarantine ./minwon-bot`.
- 서명되지 않은 개인용 프로그램이라 경고가 뜹니다. `SHA256SUMS.txt` 로 파일이 이 페이지의 것과 같은지 확인할 수 있습니다.

### 이 빌드에서 자동으로 확인한 것
- 전체 자동 테스트 통과
- 실제 Linux, Windows, macOS(Apple Silicon) 환경에서 프로그램 기동, 첫 화면·설정·보고함, 외부 접근 차단과 토큰 없는 요청 차단, 데이터 파일 생성
- 같은 환경에서 브라우저를 창 없이 띄워 화면 구조 저장(`snapshot`) 시도(참고용 — 실행 기록에서 결과 확인)

### 확인하지 못한 것
- Mac Intel 판은 빌드만 했고 실행은 확인하지 못했습니다.
- 내 PC 에서의 첫 실행 경고 화면, 국민신문고 자동 제출(아직 없음).
