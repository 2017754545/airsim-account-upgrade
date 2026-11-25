package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMU300SelfNumberPrefix(t *testing.T) {
	for in, want := range map[string]string{"8615550001111": "+8615550001111", "008615550001111": "+8615550001111", "+8615550001111": "+8615550001111", "15550001111": "15550001111", "": "", "86123": "86123", "0086123": "0086123", "0033123456789": "0033123456789"} {
		if got := normalizeMU300SelfNumber(in); got != want {
			t.Errorf("normalization mismatch: %q", in)
		}
	}
}
func TestMU300SMSPoolKnownLayouts(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "etc/mu300/sms-pool/msg")
	next := filepath.Join(root, "etc/mu300/sms/msg")
	if mu300SMSPool(root) != old {
		t.Fatal("unexpected fallback")
	}
	if err := os.MkdirAll(next, 0700); err != nil {
		t.Fatal(err)
	}
	if mu300SMSPool(root) != next {
		t.Fatal("new layout ignored")
	}
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, old); err != nil {
		t.Fatal(err)
	}
	if mu300SMSPool(root) != next {
		t.Fatal("linked pool accepted")
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	if mu300SMSPool(root) != old {
		t.Fatal("existing U30 layout priority changed")
	}
}
