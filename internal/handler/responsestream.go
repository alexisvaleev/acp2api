package handler

import (
	"net/http"
	"strings"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/openai"
	"github.com/quonaro/acp2api/internal/session"
)

// streamResponse runs the turn and emits the Responses API named-event stream.
//
// The event order is the one clients need to assemble the response: create, add
// the item, add its content part, stream the deltas, close the part and the
// item, then complete. A function call replaces the message item.
func (s *Server) streamResponse(w http.ResponseWriter, r *http.Request, plan responsePlan) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, openai.ErrTypeServer, "stream_unsupported",
			"this server cannot stream responses", "")
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emitter := openai.NewEventEmitter(w)
	send := func(event openai.ResponseEvent) {
		if err := emitter.Emit(event); err == nil {
			flusher.Flush()
		}
	}

	started := plan.build(session.Result{}, nil, openai.StepLog{}, "")
	started.Status = "in_progress"
	send(openai.ResponseEvent{Type: openai.EventCreated, Response: started})
	send(openai.ResponseEvent{Type: openai.EventInProgress, Response: started})

	messageID := openai.NewID("msg")
	const messageIndex = 0
	const contentIndex = 0
	itemOpened := false

	openMessage := func() {
		if itemOpened {
			return
		}
		itemOpened = true
		send(openai.ResponseEvent{
			Type:        openai.EventOutputItemAdded,
			OutputIndex: openai.Index(messageIndex),
			Item: &openai.OutputItem{
				Type: openai.ItemMessage, ID: messageID, Status: "in_progress", Role: "assistant",
			},
		})
		send(openai.ResponseEvent{
			Type:         openai.EventContentPartAdded,
			OutputIndex:  openai.Index(messageIndex),
			ItemID:       messageID,
			ContentIndex: openai.Index(contentIndex),
			Part:         &openai.OutputContent{Type: openai.PartOutputText, Text: ""},
		})
	}

	closeMessage := func(final string) {
		send(openai.ResponseEvent{
			Type:         openai.EventOutputTextDone,
			OutputIndex:  openai.Index(messageIndex),
			ItemID:       messageID,
			ContentIndex: openai.Index(contentIndex),
			Text:         final,
		})
		send(openai.ResponseEvent{
			Type:         openai.EventContentPartDone,
			OutputIndex:  openai.Index(messageIndex),
			ItemID:       messageID,
			ContentIndex: openai.Index(contentIndex),
			Part:         &openai.OutputContent{Type: openai.PartOutputText, Text: final},
		})
		item := openai.NewMessageItem(final)
		item.ID = messageID
		send(openai.ResponseEvent{
			Type:        openai.EventOutputItemDone,
			OutputIndex: openai.Index(messageIndex),
			Item:        &item,
		})
	}

	hold := openai.NewToolStream(plan.tools)
	var text strings.Builder
	var steps openai.StepLog
	tools := newTurnLog(s.log, plan.conversationID)
	plan.turn.OnSession = tools.bind

	result, err := s.manager.Prompt(r.Context(), plan.turn, func(u acp.SessionUpdate) error {
		piece, _, step := tools.update(u)
		steps.Add(step)
		if piece == "" {
			return nil
		}
		emit := hold.Push(piece)
		if emit == "" {
			return nil
		}
		openMessage()
		text.WriteString(emit)
		send(openai.ResponseEvent{
			Type:         openai.EventOutputTextDelta,
			OutputIndex:  openai.Index(messageIndex),
			ItemID:       messageID,
			ContentIndex: openai.Index(contentIndex),
			Delta:        emit,
		})
		return nil
	})

	if err != nil {
		failed := plan.build(session.Result{}, nil, steps, "")
		failed.Status = openai.StatusFailed
		failed.Error = &openai.ErrorBody{
			Message: err.Error(),
			Type:    openai.ErrTypeServer,
			Code:    "agent_error",
		}
		send(openai.ResponseEvent{Type: openai.EventCompleted, Response: failed})
		return
	}

	rest, calls := hold.Finish()
	calls = openai.LimitCalls(calls, plan.req.ParallelToolCalls)
	tools.callerCalls(calls)
	if rest != "" {
		openMessage()
		text.WriteString(rest)
		send(openai.ResponseEvent{
			Type:         openai.EventOutputTextDelta,
			OutputIndex:  openai.Index(messageIndex),
			ItemID:       messageID,
			ContentIndex: openai.Index(contentIndex),
			Delta:        rest,
		})
	}
	tools.answered(text.String(), result.StopReason)

	items := make([]openai.OutputItem, 0, len(calls)+1)
	nextIndex := 0

	if len(calls) == 0 {
		openMessage()
		closeMessage(text.String())
		items = append(items, openai.NewMessageItem(text.String()))
	} else {
		// Prose before a call is real output, so the message item stays.
		if itemOpened {
			closeMessage(text.String())
			items = append(items, openai.NewMessageItem(text.String()))
			nextIndex = 1
		}
		for i, call := range calls {
			item := openai.NewFunctionCallItem(call)
			index := nextIndex + i
			send(openai.ResponseEvent{
				Type:        openai.EventOutputItemAdded,
				OutputIndex: openai.Index(index),
				Item:        &item,
			})
			send(openai.ResponseEvent{
				Type:        openai.EventFunctionArgsDelta,
				OutputIndex: openai.Index(index),
				ItemID:      item.ID,
				Delta:       item.Arguments,
			})
			send(openai.ResponseEvent{
				Type:        openai.EventFunctionArgsDone,
				OutputIndex: openai.Index(index),
				ItemID:      item.ID,
				Arguments:   item.Arguments,
			})
			send(openai.ResponseEvent{
				Type:        openai.EventOutputItemDone,
				OutputIndex: openai.Index(index),
				Item:        &item,
			})
			items = append(items, item)
		}
	}

	completed := plan.build(result, items, steps, text.String())
	if s.shouldStore(plan.req) {
		s.responses.put(completed.ID, result.ConversationID, completed)
	}
	send(openai.ResponseEvent{Type: openai.EventCompleted, Response: completed})
}
