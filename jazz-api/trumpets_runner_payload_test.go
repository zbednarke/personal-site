package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/runner_shadow_run.json is the run the daily runner submits after 80
// shadow-mode page readings on fictional dealer pages, one of them with a
// 300× "通貨" currency and a long non-ASCII price text
// (tools/trumpets/test_dealer_pages.py ApiByteLimitTests regenerates and
// checks it). The API must accept it: its limits are bytes.
func TestRunnerShadowPayloadPassesValidation(t *testing.T) {
	raw, err := os.ReadFile("testdata/runner_shadow_run.json")
	if err != nil {
		t.Fatal(err)
	}
	var input trumpetRunInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	input.StartedAt = nil // the fixture's start time is in the past
	if err := validateTrumpetRun(&input); err != nil {
		t.Fatalf("runner payload rejected: %v", err)
	}
	if len(input.CheckFailures) != 81 {
		t.Fatalf("check failures: %d", len(input.CheckFailures))
	}
	for i, f := range input.CheckFailures {
		if len(f.Reason) > 500 {
			t.Fatalf("reason %d is %d bytes", i, len(f.Reason))
		}
	}
	if strings.Contains(string(raw), "通貨") {
		t.Fatal("the model's currency was echoed")
	}
}

// The fix list groups failed checks by the first line of the reason; the
// runner puts page-reading detail on a second line.
func TestFixReasonIgnoresThePageReadingLine(t *testing.T) {
	first := "page did not expose a single verifiable offer (price, currency and stock)"
	for _, detail := range []string{
		`page reading (shadow, not applied): "$4,350.00 USD" USD, in stock; agrees with the stored price`,
		`page reading rejected: price text not on the page (shadow: read "4.350,00 €" EUR; differs from the stored 4400 EUR)`,
	} {
		if got := fixReason(first+"\n"+detail, false); got != first {
			t.Fatalf("grouping key %q", got)
		}
	}
}
