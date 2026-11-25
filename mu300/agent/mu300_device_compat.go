package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Normalize only an explicit Chinese international prefix. An unqualified
// local number remains unchanged: neither the UI locale nor the device model
// proves the SIM country. Do not change dial targets or SMS recipients here.
var mu300ChinaSelfNumber = regexp.MustCompile(`^861[3-9][0-9]{9}$`)

func normalizeMU300SelfNumber(number string) string {
	number = strings.TrimSpace(number)
	international := number
	if strings.HasPrefix(international, "00") {
		international = strings.TrimPrefix(international, "00")
	}
	if mu300ChinaSelfNumber.MatchString(international) {
		return "+" + international
	}
	return number
}
func mu300SMSPool(root string) string {
	for _, p := range []string{"/etc/mu300/sms-pool/msg", "/etc/mu300/sms/msg"} {
		path := filepath.Join(root, p)
		if st, err := os.Lstat(path); err == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
			return path
		}
	}
	// Do not create pool directories, follow symlinks or edit the native daemon.
	return filepath.Join(root, "/etc/mu300/sms-pool/msg")
}
