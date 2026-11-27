// Package notify emits OSC 7501 program status reports and focus-aware
// attention notifications (BEL / OSC 9).
package notify

import (
	"fmt"
	"io"
	"strings"
)

// ProgramState represents OSC 7501 terminal program status.
type ProgramState string

const (
	StateWorking ProgramState = "working"
	StateWaiting ProgramState = "waiting"
	StateDone    ProgramState = "done"
	StateFailed  ProgramState = "failed"
	StateClear   ProgramState = "clear"
)

// EmitProgramStatus writes an OSC 7501 status sequence unless disabled.
func EmitProgramStatus(w io.Writer, setting string, state ProgramState, isTTY bool) {
	if w == nil || !isTTY || setting == "off" {
		return
	}
	_, _ = fmt.Fprintf(w, "\x1b]7501;%s\a", state)
}

// EmitAttention notifies the user when work stops, respecting focus and notify mode.
// mode: "auto" (BEL only when unfocused), "bel" (always BEL), "osc9" (desktop notice w/ preview), "off".
func EmitAttention(w io.Writer, mode string, focused bool, preview string, isTTY bool) {
	if w == nil || !isTTY || mode == "off" {
		return
	}
	switch mode {
	case "bel":
		_, _ = io.WriteString(w, "\a")
	case "osc9":
		msg := sanitizePreview(preview)
		if msg == "" {
			msg = "gax: ready"
		} else {
			msg = "gax: " + msg
		}
		_, _ = fmt.Fprintf(w, "\x1b]9;%s\a", msg)
	default: // "auto"
		if !focused {
			_, _ = io.WriteString(w, "\a")
		}
	}
}

func sanitizePreview(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
