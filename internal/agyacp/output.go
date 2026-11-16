package agyacp

import "strings"

// Hold split CLI diagnostic lines until their upstream status can be classified.
func (t *Turn) output(event Event, final bool) []Event {
	if event.Thought || event.Text == "" && !final {
		return []Event{event}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	text := t.banner + event.Text
	t.banner = ""
	if text == "" {
		return nil
	}
	at := strings.Index(text, "⚠")
	if at >= 0 && (at == 0 || text[at-1] == '\n') {
		warning := text[at:]
		complete := strings.Contains(warning, "\n") || strings.Contains(warning, "Resource has been exhausted (e.g. check quota).") || final || len(warning) > 4096
		events := []Event{}
		if strings.TrimSpace(text[:at]) != "" {
			events = append(events, Event{Text: text[:at]})
		}
		if !complete {
			t.banner = warning
			return events
		}
		return append(events, Event{Text: warning})
	}

	if text == "" {
		return nil
	}
	return []Event{{Text: text}}
}
