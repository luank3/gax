package transport

import (
	"strings"
	"testing"
)

// collect feeds chunks through one parser and returns the delivered events.
func collect(t *testing.T, chunks ...string) []SSEEvent {
	t.Helper()
	var events []SSEEvent
	parser := NewSSEParser(func(event SSEEvent) bool {
		events = append(events, event)
		return true
	})
	for _, chunk := range chunks {
		parser.Feed([]byte(chunk))
	}
	parser.Finalize()
	return events
}

func TestSSEParserReadsFieldsAndEvents(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		first SSEEvent
		count int
	}{
		{
			name:  "named event with data",
			body:  "event: message\ndata: {\"a\":1}\n\n",
			first: SSEEvent{Name: "message", Data: `{"a":1}`},
			count: 1,
		},
		{
			name:  "data only defaults to an unnamed event",
			body:  "data: hello\n\n",
			first: SSEEvent{Data: "hello"},
			count: 1,
		},
		{
			name:  "one space after the colon is stripped",
			body:  "data:  two spaces\n\n",
			first: SSEEvent{Data: " two spaces"},
			count: 1,
		},
		{
			name:  "a field without a space keeps its value intact",
			body:  "data:no-space\n\n",
			first: SSEEvent{Data: "no-space"},
			count: 1,
		},
		{
			name:  "multi-line data joins with a newline",
			body:  "data: one\ndata: two\ndata: three\n\n",
			first: SSEEvent{Data: "one\ntwo\nthree"},
			count: 1,
		},
		{
			name:  "comments and unknown fields are ignored",
			body:  ": keep-alive\nid: 7\nretry: 1000\ndata: payload\n\n",
			first: SSEEvent{Data: "payload"},
			count: 1,
		},
		{
			name:  "CRLF line endings are trimmed",
			body:  "event: message\r\ndata: value\r\n\r\n",
			first: SSEEvent{Name: "message", Data: "value"},
			count: 1,
		},
		{
			name:  "a final event needs no trailing blank line",
			body:  "event: message\ndata: last",
			first: SSEEvent{Name: "message", Data: "last"},
			count: 1,
		},
		{
			name:  "an empty data field alone emits nothing",
			body:  "data\n\ndata: real\n\n",
			first: SSEEvent{Data: "real"},
			count: 1,
		},
		{
			name:  "blank lines between events are harmless",
			body:  "\n\ndata: one\n\n\n\ndata: two\n\n",
			first: SSEEvent{Data: "one"},
			count: 2,
		},
		{
			name:  "the event name resets between events",
			body:  "event: first\ndata: one\n\ndata: two\n\n",
			first: SSEEvent{Name: "first", Data: "one"},
			count: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := collect(t, test.body)
			if len(events) != test.count {
				t.Fatalf("events = %+v, want %d", events, test.count)
			}
			if events[0] != test.first {
				t.Fatalf("first event = %+v, want %+v", events[0], test.first)
			}
		})
	}
}

func TestSSEParserEventsDoNotDependOnChunkBoundaries(t *testing.T) {
	body := ": comment\r\nevent: message\r\ndata: {\"a\":1}\r\ndata: second line\r\n\r\n" +
		"data: tail"
	want := collect(t, body)
	if len(want) != 2 {
		t.Fatalf("reference events = %+v, want 2", want)
	}
	for split := 0; split <= len(body); split++ {
		got := collect(t, body[:split], body[split:])
		if len(got) != len(want) {
			t.Fatalf("split at %d gave %+v, want %+v", split, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("split at %d event %d = %+v, want %+v", split, index, got[index], want[index])
			}
		}
	}
	byteWise := make([]string, 0, len(body))
	for index := range body {
		byteWise = append(byteWise, body[index:index+1])
	}
	got := collect(t, byteWise...)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("byte-wise events = %+v, want %+v", got, want)
	}
}

func TestSSEParserStopsDeliveryAfterTheCallbackRefuses(t *testing.T) {
	var delivered []string
	parser := NewSSEParser(func(event SSEEvent) bool {
		delivered = append(delivered, event.Data)
		return false
	})
	parser.Feed([]byte("data: first\n\ndata: second\n\n"))
	parser.Finalize()
	if len(delivered) != 1 || delivered[0] != "first" {
		t.Fatalf("delivered = %v, want only the first event", delivered)
	}

	// A refused stream stays refused through Finalize's flush.
	parser.Feed([]byte("data: third"))
	parser.Finalize()
	if len(delivered) != 1 {
		t.Fatalf("delivered = %v, want the stopped stream to stay silent", delivered)
	}
}

func TestSSEParserToleratesAMissingCallback(t *testing.T) {
	parser := NewSSEParser(nil)
	parser.Feed([]byte("data: ignored\n\n"))
	parser.Finalize()

	// The parser must also accept bytes after an empty Finalize.
	parser.Finalize()
	parser.Feed([]byte("data: later\n\n"))
	parser.Finalize()
}

func TestSSEParserDataAllocationIsReused(t *testing.T) {
	var data []string
	parser := NewSSEParser(func(event SSEEvent) bool {
		data = append(data, event.Data)
		return true
	})
	parser.Feed([]byte("data: alpha\n\ndata: beta\n\n"))
	parser.Finalize()
	if strings.Join(data, ",") != "alpha,beta" {
		t.Fatalf("data = %v, want the two events to stay distinct", data)
	}
}
