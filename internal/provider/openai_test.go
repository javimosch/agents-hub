package provider

import "testing"

// A provider that keeps only the first system message must still see the
// second one's content -- that is the failure this merge exists to prevent,
// and it is invisible at runtime because nothing errors.
func TestMergeSystemCollapsesLeadingRun(t *testing.T) {
	got := mergeSystem([]Message{
		{Role: RoleSystem, Content: "you are helper"},
		{Role: RoleSystem, Content: "recalled memory"},
		{Role: RoleUser, Content: "question"},
	})
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
	if got[0].Role != RoleSystem || got[0].Content != "you are helper\n\nrecalled memory" {
		t.Fatalf("system messages not merged: %q", got[0].Content)
	}
	if got[1].Role != RoleUser {
		t.Fatalf("user message lost: %+v", got[1])
	}
}

// A system message that appears later in a conversation is not part of the
// leading run and must be left where it is.
func TestMergeSystemLeavesLaterSystemAlone(t *testing.T) {
	in := []Message{
		{Role: RoleSystem, Content: "a"},
		{Role: RoleUser, Content: "q"},
		{Role: RoleSystem, Content: "b"},
	}
	got := mergeSystem(in)
	if len(got) != 3 || got[2].Content != "b" {
		t.Fatalf("later system message moved: %+v", got)
	}
}

func TestMergeSystemSingleIsUntouched(t *testing.T) {
	in := []Message{{Role: RoleSystem, Content: "a"}, {Role: RoleUser, Content: "q"}}
	if got := mergeSystem(in); len(got) != 2 || got[0].Content != "a" {
		t.Fatalf("single system message rewritten: %+v", got)
	}
}
