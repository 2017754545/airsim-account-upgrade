package main

import (
	"fmt"
	"testing"
	"time"
)

// Documentation-only addresses and synthetic subscribers; no real call I/O.
func TestMU300SIPAnswerKeepsIncomingIdentity(t *testing.T) {
	for _, firstIsURI := range []bool{true, false} {
		t.Run(fmt.Sprint(firstIsURI), func(t *testing.T) {
			plain, uri := "+12025550123", "sip:+12025550123@[2001:db8::1]"
			first, second := uri, plain
			if !firstIsURI {
				first, second = plain, uri
			}
			state := fmt.Sprintf("+CLCC: 1,1,4,0,0,%q,145\nOK", first)
			answers := 0
			a := testMU300Calls(t, func(cmd string, _ time.Duration) (string, error) {
				switch cmd {
				case "AT+CLCC":
					return state, nil
				case "ATA":
					answers++
					state = fmt.Sprintf("+CLCC: 1,1,0,0,0,%q,145\nOK", second)
					return "OK", nil
				default:
					t.Fatalf("unexpected command %q", cmd)
					return "", nil
				}
			})
			if _, err := a.mu300RefreshCalls(); err != nil {
				t.Fatal(err)
			}
			id := a.calls.Active.ID
			if err := a.answerCallForID(id); err != nil {
				t.Fatalf("same caller rejected during answer: %v", err)
			}
			if answers != 1 || a.calls.Active.ID != id || a.calls.Active.AndroidID != id || a.calls.Active.State != "active" || len(a.calls.History) != 0 {
				t.Fatal("answer replaced the logical call or emitted a false terminal event")
			}
			if a.calls.Active.Number != plain {
				t.Fatal("SIP address leaked into display number")
			}
		})
	}
}

func TestMU300CallerNumberProjection(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"sip:+12025550123@[2001:db8::1]", "+12025550123"},
		{"SIPS:%2B1-202-555-0123@ims.example:5061;user=phone;transport=tls", "+12025550123"},
		{"sip:12345@192.0.2.1:5060", "12345"},
		{"sip:12345@[2001:db8::2]:5060;transport=udp", "12345"},
		{"12345", "12345"},
		{"+12025550123", "+12025550123"},
		{"", ""},
	} {
		if got := mu300CallerNumber(tc.input); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.input, got, tc.want)
		}
	}
	// Invalid, nontelephone, or semantically qualified addresses stay opaque.
	for _, raw := range []string{
		"sip:alice@ims.example", "sip:anonymous@anonymous.invalid",
		"sip:12345:password@ims.example", "sip:12345;phone-context=example.org@ims.example",
		"sip:12345;ext=1@ims.example", "sip:12345@ims.example;user=ip",
		"sip:12345@ims.example;user=phone;user=ip", "sip:12345@ims.example;ext=1",
		"sip:12345@", "sip:12345@[not-ipv6]", "sip:12345@2001:db8::1",
		"sip:12345@ims.example:0", "sip:12345@ims.example:65536", "sip:12345@ims.example:",
		"sip:12345@ims.example/path", "sip:12345@ims.example?header=value", "sip:12345@ims.example#",
		"sip:12345@@ims.example", "sip:12%0A345@ims.example", "sip:12%ZZ345@ims.example",
		"sip:12345@ims..example", "sip:12345@-ims.example", "sip:*123#@ims.example",
		"sip:123456789012345678901@ims.example", "tel:12345;phone-context=example.org",
	} {
		if got := mu300CallerNumber(raw); got != raw {
			t.Errorf("unsafe projection: %q => %q", raw, got)
		}
	}
	if mu300CallerIdentity("sip:12345@ims.example") == mu300CallerIdentity("+12345") {
		t.Fatal("guessed a country/global prefix")
	}
	if normalizeDialNumber("sip:12345@ims.example") != "" {
		t.Fatal("SIP unexpectedly became dialable input")
	}
}

func TestMU300SIPCallerReplacementStillRetiresOldID(t *testing.T) {
	for _, tc := range []struct{ first, second string }{
		{"sip:12345@[2001:db8::1]", "sip:54321@[2001:db8::1]"},
		{"sip:alice@ims.example", "sip:bob@ims.example"},
	} {
		a := testMU300Calls(t, nil)
		now := time.Now()
		a.mu300ApplyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: tc.first}}, now, true)
		id := a.calls.Active.ID
		a.mu300ApplyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: tc.second}}, now.Add(time.Second), true)
		if a.calls.Active.ID == id || len(a.calls.History) != 1 || a.calls.History[0].ID != id {
			t.Fatal("a different SIP caller inherited the old identity")
		}
	}
}

func TestMU300SIPSameSubscriberDifferentHostKeepsIDButSlotReuseDoesNot(t *testing.T) {
	a := testMU300Calls(t, nil)
	now := time.Now()
	poll := func(state, number string) {
		now = now.Add(time.Second)
		a.mu300ApplyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: state, Number: number}}, now, true)
	}
	poll("incoming", "sip:12345@[2001:db8::1]")
	id := a.calls.Active.ID
	poll("active", "sip:12345@[2001:db8::2]")
	if a.calls.Active.ID != id || len(a.calls.History) != 0 {
		t.Fatal("IMS host change retired a live call")
	}
	poll("incoming", "sip:12345@[2001:db8::2]")
	if a.calls.Active.ID == id || len(a.calls.History) != 1 {
		t.Fatal("reused slot kept the connected call identity")
	}
}
