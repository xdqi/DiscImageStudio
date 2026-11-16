package dvd

import (
	"strings"
	"testing"
)

// TestParseCliArgumentsRepeatable: options declared repeatable accumulate all
// their values (case-insensitively); every other option keeps the C#
// duplicate rejection.
func TestParseCliArgumentsRepeatable(t *testing.T) {
	args, err := parseCliArguments(
		[]string{"--expose", "a/bar", "--EXPOSE=a/b", "--volume-label", "X", "--expose", "c"},
		"expose")
	if err != nil {
		t.Fatal(err)
	}
	got := args.GetAll("expose")
	want := []string{"a/bar", "a/b", "c"}
	if len(got) != len(want) {
		t.Fatalf("GetAll(expose) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GetAll(expose)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if args.Get("expose", "") != "a/bar" {
		t.Fatalf("Get(expose) = %q, want the first value", args.Get("expose", ""))
	}
	if !args.Has("expose") {
		t.Fatal("Has(expose) = false")
	}
	if args.Get("volume-label", "") != "X" {
		t.Fatalf("Get(volume-label) = %q", args.Get("volume-label", ""))
	}

	// Non-repeatable options still reject duplicates (C# CliArguments).
	if _, err := parseCliArguments([]string{"--expose", "a", "--expose", "b"}); err == nil ||
		!strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate without repeatable: %v", err)
	}
	if _, err := parseCliArguments([]string{"--volume-label", "A", "--volume-label", "B"}, "expose"); err == nil ||
		!strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate non-repeatable: %v", err)
	}
}
