package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Select once before submission; never probe by submitting or retry another mode.
// The optional root-owned profile also supports vendor scripts with unknown syntax.
const mu300SMSModePath = "/etc/mu300/airsim-sms-send-mode"

var mu300StdinOption = regexp.MustCompile(`(?m)(?:^|[; \t])(?:["']?--stdin["']?\s*\)|[^\n]*[=!]=?\s*["']--stdin["'])`)
var mu300PositionalUsage = regexp.MustCompile(`(?i)send\s+(?:<|\[)?(?:num|number|phone)(?:>|\])?\s+(?:<|\[)?(?:text|message)(?:>|\])?`)

func mu300SMSSendMode(script []byte, configured string) (string, error) {
	switch configured {
	case "stdin", "positional":
		return configured, nil
	case "", "auto":
	default:
		return "", errors.New("invalid native SMS send mode")
	}
	// Unknown/binary wrappers retain the original stdin contract; no speculative
	// positional submission. A failed stdin invocation is never auto-retried.
	if !strings.HasPrefix(string(script), "#!") {
		return "stdin", nil
	}
	var lines []string
	for _, line := range strings.Split(string(script), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			lines = append(lines, line)
		}
	}
	code := strings.Join(lines, "\n")
	if mu300StdinOption.MatchString(code) {
		return "stdin", nil
	}
	if mu300PositionalUsage.MatchString(code) || (strings.Contains(code, "send)") && strings.Contains(code, "send needs a phone number and a text")) {
		return "positional", nil
	}
	return "stdin", nil
}
func mu300SMSCommand(ctx context.Context, executable, mode, input string) (*exec.Cmd, error) {
	phone, text, ok := strings.Cut(input, "\n")
	if !ok || !mu300SMSPhone.MatchString(phone) || text == "" {
		return nil, errors.New("invalid SMS send input")
	}
	switch mode {
	case "stdin":
		cmd := exec.CommandContext(ctx, executable, "send", "--stdin")
		cmd.Stdin = strings.NewReader(input)
		return cmd, nil
	case "positional":
		// exec args, not a shell string. This vendor API exposes its arguments to local
		// process inspection; stdout/stderr are never returned to clients or logged.
		return exec.CommandContext(ctx, executable, "send", phone, text), nil
	}
	return nil, errors.New("invalid SMS send mode")
}
func runMU300SMSSend(ctx context.Context, input string) (string, error) {
	const executable = "/opt/mu300/bin/mu300-sms"
	configured, err := readMU300PrivateFile(mu300SMSModePath, 64)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("native SMS send profile unavailable")
	}
	var script []byte
	if f, e := os.Open(executable); e == nil {
		script, _ = io.ReadAll(io.LimitReader(f, 262145))
		f.Close()
	}
	if len(script) > 262144 {
		script = nil
	}
	mode, err := mu300SMSSendMode(script, strings.TrimSpace(string(configured)))
	if err != nil {
		return "", err
	}
	cmd, err := mu300SMSCommand(ctx, executable, mode, input)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	return string(out), err
}

// Only a complete final receipt line plus successful process exit is accepted.
// Earlier diagnostic output may mention recipients; never expose that output.
func mu300SMSReceipt(out string) bool {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return mu300SentReply.MatchString(strings.TrimSpace(lines[len(lines)-1]))
}
