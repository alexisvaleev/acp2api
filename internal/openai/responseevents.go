package openai

import (
	"encoding/json"
	"io"
)

// Responses API streaming event names.
const (
	EventCreated           = "response.created"
	EventInProgress        = "response.in_progress"
	EventOutputItemAdded   = "response.output_item.added"
	EventContentPartAdded  = "response.content_part.added"
	EventOutputTextDelta   = "response.output_text.delta"
	EventOutputTextDone    = "response.output_text.done"
	EventContentPartDone   = "response.content_part.done"
	EventOutputItemDone    = "response.output_item.done"
	EventFunctionArgsDelta = "response.function_call_arguments.delta"
	EventFunctionArgsDone  = "response.function_call_arguments.done"
	EventCompleted         = "response.completed"
)

// ResponseEvent is one server-sent event of the Responses API.
//
// Unlike chat-completions SSE, each event carries its own name on the `event:`
// line, and clients assemble the response from the item and delta events.
type ResponseEvent struct {
	Type           string         `json:"type"`
	SequenceNumber int            `json:"sequence_number"`
	Response       *Response      `json:"response,omitempty"`
	OutputIndex    *int           `json:"output_index,omitempty"`
	ItemID         string         `json:"item_id,omitempty"`
	Item           *OutputItem    `json:"item,omitempty"`
	ContentIndex   *int           `json:"content_index,omitempty"`
	Part           *OutputContent `json:"part,omitempty"`
	Delta          string         `json:"delta,omitempty"`
	Text           string         `json:"text,omitempty"`
	Arguments      string         `json:"arguments,omitempty"`
}

// EventEmitter writes named Responses API events with a monotonic sequence.
type EventEmitter struct {
	w        io.Writer
	sequence int
}

// NewEventEmitter creates an emitter over w.
func NewEventEmitter(w io.Writer) *EventEmitter {
	return &EventEmitter{w: w}
}

// Emit writes one event, filling in its sequence number and type.
func (e *EventEmitter) Emit(event ResponseEvent) error {
	e.sequence++
	event.SequenceNumber = e.sequence

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(e.w, "event: "+event.Type+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(e.w, "data: "); err != nil {
		return err
	}
	if _, err := e.w.Write(data); err != nil {
		return err
	}
	_, err = io.WriteString(e.w, "\n\n")
	return err
}

// Index returns a pointer to n, for the optional index fields.
func Index(n int) *int { return &n }
