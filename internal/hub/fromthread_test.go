package hub

import (
	"reflect"
	"testing"

	"github.com/javimosch/agents-hub/internal/eval"
	"github.com/javimosch/agents-hub/internal/provider"
)

func TestWhatAThreadDidBecomesAnOutcomeToCheck(t *testing.T) {
	got, err := ReadThread([]Message{
		{Role: provider.RoleUser, Content: "check it"},
		{Role: provider.RoleAssistant, ToolCalls: storedCalls("a", "b"), Prompt: 10, Completed: 2},
		{Role: provider.RoleTool, ToolCallID: "a", Content: "r"},
		{Role: provider.RoleTool, ToolCallID: "b", Content: "r"},
		{Role: provider.RoleAssistant, Content: "all good", Prompt: 20, Completed: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := ThreadRecord{Question: "check it", Questions: 1,
		Outcome: eval.Outcome{Answer: "all good", Tokens: 35, Steps: 2, Tools: []string{"noop", "noop"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// A case sends one message, so only the first exchange can be replayed.
func TestOnlyTheFirstExchangeOfAThreadIsRead(t *testing.T) {
	got, err := ReadThread([]Message{
		{Role: provider.RoleUser, Content: "one"}, {Role: provider.RoleAssistant, Content: "first"},
		{Role: provider.RoleUser, Content: "two"}, {Role: provider.RoleAssistant, Content: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Question != "one" || got.Outcome.Answer != "first" || got.Questions != 2 {
		t.Errorf("got %+v, want the first exchange and a count of 2", got)
	}
}

func TestAThreadWithoutAQuestionCannotBecomeACase(t *testing.T) {
	if _, err := ReadThread([]Message{{Role: provider.RoleAssistant, Content: "hi"}}); err == nil {
		t.Error("want an error")
	}
}
