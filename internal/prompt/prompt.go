// Package prompt 提供交互式输入工具。
//
// 非 TTY 环境调用任何 prompt 都返回 NOT_INTERACTIVE，避免在管道里挂起。
package prompt

import (
	"bufio"
	"io"
	"os"
	"strings"

	"github.com/YoooClaw/cli/internal/errs"
)

// IsInteractive 报告 stdin 与 stdout 是否都是 TTY。
func IsInteractive() bool {
	return isTTY(os.Stdin) && isTTY(os.Stdout)
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func ensureInteractive() error {
	if !IsInteractive() {
		return errs.New(errs.CodeNotInteractive, "当前为非交互环境，无法进行交互式输入").
			WithHint("改用 --non-interactive --from-file，或在 TTY 中运行")
	}
	return nil
}

// Ask 提一个问题，返回去空白后的回答；提供 defaultValue 时回车采用默认。
func Ask(question, defaultValue string) (string, error) {
	if err := ensureInteractive(); err != nil {
		return "", err
	}
	return ask(os.Stdin, os.Stdout, question, defaultValue)
}

// ask 是 Ask 的纯 I/O 核心（注入 reader/writer 以便单测）。
func ask(in io.Reader, out io.Writer, question, defaultValue string) (string, error) {
	suffix := ""
	if defaultValue != "" {
		suffix = " [" + defaultValue + "]"
	}
	_, _ = io.WriteString(out, question+suffix+": ")
	reader := bufio.NewReader(in)
	line, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(line)
	if answer == "" {
		return defaultValue, nil
	}
	return answer, nil
}

// Confirm 是 Yes/No 确认，默认值由 defaultYes 决定。
func Confirm(question string, defaultYes bool) (bool, error) {
	if err := ensureInteractive(); err != nil {
		return false, err
	}
	return confirm(os.Stdin, os.Stdout, question, defaultYes)
}

// confirm 是 Confirm 的纯 I/O 核心（注入 reader/writer 以便单测）。
func confirm(in io.Reader, out io.Writer, question string, defaultYes bool) (bool, error) {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	answer, err := ask(in, out, question+" ("+hint+")", "")
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(answer)
	if answer == "" {
		return defaultYes, nil
	}
	return answer == "y" || answer == "yes", nil
}

// ReadStdin 读取 stdin 全部内容（用于 --from-file - / set-api-key -）。
func ReadStdin() (string, error) {
	return readAll(os.Stdin)
}

func readAll(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
