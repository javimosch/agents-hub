package hub

// Event is something that happened during a turn, reported as it happens so a
// caller watching a long run can follow it. The tags are the --stream wire
// shape.
type Event struct {
	Type   string `json:"type"` // start, tool_call or tool_result
	Thread string `json:"thread,omitempty"`
	Step   int    `json:"step,omitempty"`
	ID     string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	Chars  int    `json:"chars,omitempty"`
	Failed string `json:"failed,omitempty"`
}

func (o ChatOptions) emit(e Event) {
	if o.OnEvent != nil {
		o.OnEvent(e)
	}
}
