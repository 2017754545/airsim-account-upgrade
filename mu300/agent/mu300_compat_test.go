package main

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMU300SMSModesAndArguments(t *testing.T) {
	for _, tc := range []struct{ script, profile, want string }{
		{"#!/bin/sh\ncase $2 in --stdin) read number;; esac", "", "stdin"},
		{"#!/bin/sh\n# --stdin is unsupported\ncase $1 in\nsend) die 'send needs a phone number and a text';;\nesac", "", "positional"},
		{"#!/bin/sh\necho 'usage: mu300-sms send <number> <text>'", "auto", "positional"},
		{"ELF fixture", "", "stdin"}, {"unrecognized", "positional", "positional"}, {"unrecognized", "stdin", "stdin"},
	} {
		mode, err := mu300SMSSendMode([]byte(tc.script), tc.profile)
		if err != nil || mode != tc.want {
			t.Fatalf("mode=%s %v want=%s", mode, err, tc.want)
		}
	}
	if _, err := mu300SMSSendMode(nil, "shell"); err == nil {
		t.Fatal("unknown mode")
	}
	input := "+15550000001\nfixture ' $(not-run)\nsecond line"
	for _, mode := range []string{"stdin", "positional"} {
		cmd, err := mu300SMSCommand(context.Background(), "/fixture/not-executed", mode, input)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "stdin" {
			if !reflect.DeepEqual(cmd.Args, []string{"/fixture/not-executed", "send", "--stdin"}) {
				t.Fatal(cmd.Args)
			}
			b, _ := io.ReadAll(cmd.Stdin)
			if string(b) != input {
				t.Fatal("stdin payload changed")
			}
		} else if !reflect.DeepEqual(cmd.Args, []string{"/fixture/not-executed", "send", "+15550000001", "fixture ' $(not-run)\nsecond line"}) || cmd.Stdin != nil {
			t.Fatal("positional payload changed")
		}
	}
	for _, input := range []string{"no delimiter", "bad;phone\ntext", "15550000001\n"} {
		if _, err := mu300SMSCommand(context.Background(), "unused", "stdin", input); err == nil {
			t.Fatal("invalid input")
		}
	}
}
func TestMU300SMSReceiptCompatibilityAndUncertainNoRetry(t *testing.T) {
	for _, receipt := range []string{"sent (reference 7)\n", "submitting fixture\nsent (7)\r\n"} {
		a := smsFixture(t)
		calls := 0
		a.mu300SMS.runSend = func(context.Context, string) (string, error) { calls++; return receipt, nil }
		for i := 0; i < 2; i++ {
			if n, err := a.mu300SendSMS("15550000001", "fixture"); err != nil || n != 1 {
				t.Fatal(n, err)
			}
		}
		if calls != 1 {
			t.Fatal("duplicate submission")
		}
	}
	for _, receipt := range []string{"", "prefix sent (7)", "sent (7)\nERROR", "sent (private)", "sent (reference 7) trailing"} {
		if mu300SMSReceipt(receipt) {
			t.Fatal("false receipt", receipt)
		}
	}
	a := smsFixture(t)
	calls := 0
	a.mu300SMS.runSend = func(context.Context, string) (string, error) {
		calls++
		return "sent (7)", errors.New("private failure")
	}
	for i := 0; i < 2; i++ {
		if _, err := a.mu300SendSMS("15550000001", "fixture"); err == nil || strings.Contains(err.Error(), "private failure") {
			t.Fatal("uncertain result exposed or retried")
		}
	}
	if calls != 1 {
		t.Fatal("mode retried after uncertain outcome")
	}
}
func TestMU300VendorSignalParsing(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"sig":{"rsrp":-95.6}}`, -96}, {`{"lte":{"rsrp":-110}}`, -110}, {`{"sig":{"rsrp":0},"lte":{"rsrp":-90}}`, -90},
		{`{}`, 0}, {`null`, 0}, {`{"sig":{"rsrp":"-90"}}`, 0}, {`{"sig":{"rsrp":-140.1}}`, 0}, {`{"sig":{"rsrp":-29.9}}`, 0}, {`{"sig":{"rsrp":null}}`, 0}, {`{"sig":{"rsrp":-90}} trailing`, 0},
	} {
		got := parseMU300VendorSignal([]byte(tc.body))
		if tc.want == 0 {
			if got != nil {
				t.Fatal(tc.body, *got)
			}
		} else if got == nil || *got != tc.want {
			t.Fatal(tc.body, got)
		}
	}
}
func TestMU300MetadataFallbackOperatorAndStaleClear(t *testing.T) {
	csq := "+CSQ: 20,99\nOK"
	fail := false
	a := testMU300Calls(t, func(command string, _ time.Duration) (string, error) {
		if fail {
			return "", errors.New("unavailable")
		}
		switch command {
		case "AT+CPIN?":
			return "+CPIN: READY\nOK", nil
		case "AT+CEREG?":
			return "+CEREG: 0,1\nOK", nil
		case "AT+CSQ":
			return csq, nil
		case "AT+COPS?":
			return "+COPS: 0,2,\"46001\"\nOK", nil
		case "AT+CNUM":
			return "OK", nil
		default:
			t.Fatal("unexpected command", command)
			return "", nil
		}
	})
	calls := 0
	vendor := func() *int {
		calls++
		if !a.mu.TryLock() {
			t.Fatal("vendor called under modem lock")
		}
		a.mu.Unlock()
		n := -96
		return &n
	}
	a.mu300RefreshModemWithSignal(vendor)
	if calls != 0 || a.modem.SignalDBM == nil || *a.modem.SignalDBM != -73 || a.modem.SignalSource != "csq_rssi" || a.modem.Operator != "中国联通" {
		t.Fatal(a.modem)
	}
	csq = "+CSQ: 99,99\nOK"
	a.mu300RefreshModemWithSignal(vendor)
	if calls != 1 || a.modem.SignalDBM == nil || *a.modem.SignalDBM != -96 || a.modem.SignalSource != "vendor_rsrp" {
		t.Fatal(a.modem)
	}
	fail = true
	a.mu300RefreshModemWithSignal(func() *int { return nil })
	if a.modem.SignalDBM != nil || a.modem.SignalSource != "" || a.modem.Operator != "" || a.modem.SignalUpdatedAt == "" {
		t.Fatal("stale metadata retained", a.modem)
	}
	a.applyCallPoll(parseCLCC(mu300TestActive+"OK"), time.Now())
	a.mu300RefreshModemWithSignal(func() *int { t.Fatal("metadata polled during call"); return nil })
}
