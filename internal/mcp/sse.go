package mcp

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent is a single Server-Sent Events frame.
type sseEvent struct {
	Event string
	Data  string
	ID    string
}

// readSSE parses an SSE stream and calls fn for every dispatched event.
// It stops when fn returns false, the stream ends, or a read error occurs.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	var ev sseEvent
	var data []string
	dispatch := func() bool {
		if len(data) == 0 && ev.Event == "" {
			return true
		}
		ev.Data = strings.Join(data, "\n")
		if ev.Event == "" {
			ev.Event = "message"
		}
		keepGoing := fn(ev)
		ev = sseEvent{}
		data = data[:0]
		return keepGoing
	}
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if !dispatch() {
					return nil
				}
			case strings.HasPrefix(line, ":"):
				// comment / heartbeat
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					ev.Event = value
				case "data":
					data = append(data, value)
				case "id":
					ev.ID = value
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				dispatch()
				return nil
			}
			return err
		}
	}
}
