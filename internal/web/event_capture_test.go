package web

import "harness/internal/events"

func drainTestEvents(stream <-chan events.Event, sessionID string) []events.Event {
	result := []events.Event{}
	for {
		select {
		case event, ok := <-stream:
			if !ok {
				return result
			}
			if event.SessionID == sessionID {
				result = append(result, event)
			}
		default:
			return result
		}
	}
}
