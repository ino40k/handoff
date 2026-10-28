package network

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"handoff/internal/core"
)

func TestPeerResponsesAreBoundedBeforeValidation(t *testing.T) {
	for name, response := range map[string]string{
		"error":    `{"error":"` + strings.Repeat("x", maxPeerResponse) + `"}`,
		"progress": `{"done":[` + strings.Repeat("0,", maxPeerResponse/2) + `0]}`,
		"unknown":  `{"unknown":"` + strings.Repeat("x", maxPeerResponse) + `"}`,
		"trailing": `{"done":[]}` + strings.Repeat(" ", maxPeerResponse),
		"extra":    `{"done":[]} {"done":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var status TransferStatus
			if err := decodeResponse(io.NopCloser(strings.NewReader(response)), &status); err == nil {
				t.Fatal("oversized or extra peer response accepted")
			}
		})
	}
}

func TestMaximumLegitimateProgressResponse(t *testing.T) {
	want := TransferStatus{Done: make([]int64, core.MaxEntries), Approved: true, Status: "active"}
	for i := range want.Done {
		want.Done[i] = 1 << 50
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got TransferStatus
	if err = decodeResponse(io.NopCloser(strings.NewReader(string(body))), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Done) != core.MaxEntries || got.Done[core.MaxEntries-1] != 1<<50 || !got.Approved {
		t.Fatal("maximum legitimate response changed")
	}
}
