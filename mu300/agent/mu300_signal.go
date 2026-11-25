package main

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"time"
)

// Native single-SIM read-only fallback. No modem write or metadata mutex is held.
func mu300VendorSignalDBM() *int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ubus", "call", "mu300.modem", "signal", `{"slot":"0"}`).Output()
	if err != nil {
		return nil
	}
	return parseMU300VendorSignal(out)
}
func parseMU300VendorSignal(out []byte) *int {
	if len(out) > 65536 {
		return nil
	}
	type block struct {
		RSRP *float64 `json:"rsrp"`
	}
	var payload struct {
		Sig *block `json:"sig"`
		LTE *block `json:"lte"`
	}
	if json.Unmarshal(out, &payload) != nil {
		return nil
	}
	for _, candidate := range []*block{payload.Sig, payload.LTE} {
		if candidate == nil || candidate.RSRP == nil {
			continue
		}
		value := *candidate.RSRP
		if math.IsNaN(value) || math.IsInf(value, 0) || value < -140 || value > -30 {
			continue
		}
		dbm := int(math.Round(value))
		return &dbm
	}
	return nil
}
