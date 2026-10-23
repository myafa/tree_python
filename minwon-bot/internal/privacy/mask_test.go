package privacy

import (
	"strings"
	"testing"
)

func TestMaskUnmaskRoundTrip(t *testing.T) {
	m := NewMasker([]string{"홍길동", "제7사단"})
	in := "홍길동(010-1234-5678, gil@example.com)은 제7사단 소속이며 주민번호는 900101-1234567, 군번은 21-76001234, 계좌는 110-123-456789 입니다."
	masked := m.Mask(in)
	for _, secret := range []string{"홍길동", "제7사단", "010-1234-5678", "gil@example.com", "900101-1234567", "21-76001234", "110-123-456789"} {
		if strings.Contains(masked, secret) {
			t.Errorf("가려지지 않음: %s\n%s", secret, masked)
		}
	}
	if got := m.Unmask(masked); got != in {
		t.Fatalf("왕복 불일치:\n got: %s\nwant: %s", got, in)
	}
}

func TestSameValueSamePlaceholder(t *testing.T) {
	m := NewMasker(nil)
	out := m.Mask("연락처 010-1111-2222, 다시 010-1111-2222")
	if strings.Count(out, "{{PHONE_1}}") != 2 || strings.Contains(out, "PHONE_2") {
		t.Fatalf("같은 값은 같은 자리표시자여야 함: %s", out)
	}
}

func TestLongerTermFirst(t *testing.T) {
	m := NewMasker([]string{"홍길", "홍길동"})
	out := m.Mask("홍길동")
	if strings.Contains(out, "홍길") {
		t.Fatalf("긴 낱말이 먼저 처리되어야 함: %s", out)
	}
	if m.Unmask(out) != "홍길동" {
		t.Fatalf("복원 실패: %s", m.Unmask(out))
	}
}

func TestUnmaskLongPlaceholderFirst(t *testing.T) {
	m := NewMasker(nil)
	var sb strings.Builder
	for i := 1; i <= 11; i++ {
		sb.WriteString(m.token("PHONE", "010-0000-00"+strings.Repeat("1", i%7+1)+string(rune('a'+i))))
		sb.WriteString(" ")
	}
	if strings.Contains(m.Unmask(sb.String()), "{{") {
		t.Fatalf("자리표시자가 남음: %s", m.Unmask(sb.String()))
	}
}

func TestLeftover(t *testing.T) {
	if got := Leftover("안녕 {{PHONE_3}} 그리고 {{TERM_1}}"); len(got) != 2 {
		t.Fatalf("남은 자리표시자 2개여야 함: %v", got)
	}
	if got := Leftover("깨끗한 글"); len(got) != 0 {
		t.Fatalf("없어야 함: %v", got)
	}
}

func TestScanCountsOnceAndKeepsNoValues(t *testing.T) {
	f := Scan("주민번호 900101-1234567, 전화 010-1234-5678, 메일 a@b.co")
	kinds := map[string]int{}
	for _, x := range f {
		kinds[x.Kind] = x.Count
	}
	if kinds["RRN"] != 1 || kinds["PHONE"] != 1 || kinds["EMAIL"] != 1 {
		t.Fatalf("검출 불일치: %v", kinds)
	}
	if len(Scan("개인정보 없음. 2026년 10월 3건")) != 0 {
		t.Fatal("오탐")
	}
}

func TestTypedTermsAndLegend(t *testing.T) {
	m := NewMasker([]string{"이름:홍길동", "부대：제7사단", "기관:국방부", "그냥낱말", "모르는종류:값"})
	in := "홍길동 상병은 제7사단 소속이며 국방부에 그냥낱말 관련 민원을 냅니다. 모르는종류:값 도 있습니다. 010-1234-5678"
	masked := m.Mask(in)
	for _, want := range []string{"{{NAME_1}}", "{{UNIT_1}}", "{{ORG_1}}", "{{TERM_1}}", "{{TERM_2}}", "{{PHONE_1}}"} {
		if !strings.Contains(masked, want) {
			t.Errorf("%s 없음: %s", want, masked)
		}
	}
	if m.Unmask(masked) != in {
		t.Fatalf("왕복 실패: %s", m.Unmask(masked))
	}
	legend := m.Legend()
	for _, want := range []string{"{{NAME_1}}: 사람 이름", "{{UNIT_1}}: 부대·조직 이름", "{{ORG_1}}: 기관 이름", "{{PHONE_1}}: 전화번호", "<자리표시자_설명>"} {
		if !strings.Contains(legend, want) {
			t.Errorf("설명에 %q 없음:\n%s", want, legend)
		}
	}
	for _, secret := range []string{"홍길동", "제7사단", "국방부", "010-1234-5678"} {
		if strings.Contains(legend, secret) {
			t.Errorf("설명에 원문 값이 새면 안 됨: %s", secret)
		}
	}
	if NewMasker(nil).Legend() != "" {
		t.Error("만든 것이 없으면 설명도 없어야 함")
	}
}
