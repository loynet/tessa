package main

import "testing"

func TestParseCommand(t *testing.T) {
	for _, command := range []string{"run", "check-config", "check-health"} {
		if got, err := parseCommand([]string{command}); err != nil || got != command {
			t.Fatalf("parseCommand(%q) = %q, %v", command, got, err)
		}
	}
	if _, err := parseCommand([]string{"chatter"}); err == nil {
		t.Fatal("chatter command accepted")
	}
}
