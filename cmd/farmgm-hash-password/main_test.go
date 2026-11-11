package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"farmgm/internal/auth"
)

// inputs 依序回傳密碼，並記錄顯示過的提示。
func inputs(prompts *[]string, values ...string) func(string) (string, error) {
	return func(prompt string) (string, error) {
		*prompts = append(*prompts, prompt)
		if len(values) == 0 {
			return "", errors.New("EOF")
		}
		v := values[0]
		values = values[1:]
		return v, nil
	}
}

func TestHashPasswordSuccess(t *testing.T) {
	var prompts []string
	var out, errOut bytes.Buffer
	if code := run(inputs(&prompts, "correct horse", "correct horse"), &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	line := out.String()
	if !strings.HasPrefix(line, "ADMIN_PASSWORD_HASH=scrypt:") || !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		t.Fatalf("stdout=%q", line)
	}
	hash := strings.TrimSuffix(strings.TrimPrefix(line, "ADMIN_PASSWORD_HASH="), "\n")
	if !auth.VerifyPassword("correct horse", hash) || auth.VerifyPassword("wrong", hash) {
		t.Fatal("產生的雜湊無法驗證原密碼")
	}
	if strings.Join(prompts, "|") != "管理員密碼：|再輸入一次：" || errOut.Len() != 0 {
		t.Fatalf("prompts=%v stderr=%q", prompts, errOut.String())
	}
}

func TestHashPasswordRejects(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		msg    string
	}{
		{"太短", []string{"1234567"}, "密碼至少需要 8 個字元。"},
		{"不一致", []string{"12345678", "12345679"}, "兩次輸入的密碼不一致。"},
		{"第二次讀取失敗", []string{"12345678"}, "兩次輸入的密碼不一致。"},
		{"第一次讀取失敗", nil, "無法讀取密碼。"},
		// Python len() 以字元計算：7 個中文字（21 bytes）仍算太短
		{"中文字以字元計算", []string{"一二三四五六七"}, "密碼至少需要 8 個字元。"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var prompts []string
			var out, errOut bytes.Buffer
			if code := run(inputs(&prompts, c.values...), &out, &errOut); code != 1 {
				t.Fatalf("code=%d", code)
			}
			if out.Len() != 0 || strings.TrimSpace(errOut.String()) != c.msg {
				t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
			}
		})
	}
	var prompts []string
	var out bytes.Buffer
	if code := run(inputs(&prompts, "一二三四五六七八", "一二三四五六七八"), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("8 個中文字應可接受，code=%d", code)
	}
}

func TestCleanLine(t *testing.T) {
	b := string(rune(0xFEFF))
	for in, want := range map[string]string{
		"password\n":          "password",
		"password\r\n":        "password",
		"password":            "password", // 最後一行沒有換行（EOF）
		b + "password\r\n":    "password", // PowerShell 管線的 BOM
		"pass" + b + "word\n": "password",
		" spaced pw \n":       " spaced pw ", // 前後空白是密碼的一部分，不去除
	} {
		if got := cleanLine(in); got != want {
			t.Errorf("cleanLine(%q)=%q，應為 %q", in, got, want)
		}
	}
}
