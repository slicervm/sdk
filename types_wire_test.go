package slicer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWireLastAgentCallOmitzero(t *testing.T) {
	node := SlicerNode{Hostname: "vm-1"}
	b, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	if jsonContains(b, "last_agent_call") {
		t.Fatalf("unset LastAgentCall must be omitted: %s", b)
	}

	marker := time.Unix(1700000000, 0).UTC()
	node.LastAgentCall = marker
	b, err = json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContains(b, "last_agent_call") {
		t.Fatalf("set LastAgentCall must be present: %s", b)
	}
	var back SlicerNode
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.LastAgentCall.Equal(marker) {
		t.Fatalf("round-trip mismatch: got %v want %v", back.LastAgentCall, marker)
	}
}

func TestWireBgExecsOmitzero(t *testing.T) {
	desc := SlicerVMDescription{}
	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if jsonContains(b, "bg_execs") {
		t.Fatalf("nil BgExecs must be omitted: %s", b)
	}

	desc.BgExecs = []SlicerBgExecSummary{}
	b, err = json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContains(b, `"bg_execs":[]`) {
		t.Fatalf("non-nil empty BgExecs must encode as []: %s", b)
	}
	var back SlicerVMDescription
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.BgExecs == nil || len(back.BgExecs) != 0 {
		t.Fatalf("empty BgExecs round-trip: got %v", back.BgExecs)
	}

	desc.BgExecs = []SlicerBgExecSummary{{ExecID: "ex_1", State: "running"}}
	b, err = json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContains(b, "ex_1") {
		t.Fatalf("populated BgExecs must be present: %s", b)
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.BgExecs) != 1 || back.BgExecs[0].ExecID != "ex_1" {
		t.Fatalf("populated BgExecs round-trip: got %+v", back.BgExecs)
	}
}

func jsonContains(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}
