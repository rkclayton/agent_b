package agent

import "harness/internal/config"

// The hand-off uses the chat's own connection with a stable, low-temperature
// sampling profile. Context selection and the request itself live at the one
// pre-request compaction boundary.
func summaryConnection(connection *config.Connection) config.Connection {
	result := *connection
	result.Sampling.Thinking.Temperature = .3
	result.Sampling.Nonthinking.Temperature = .3
	if len(result.Reasoning.ValidEfforts) > 0 {
		result.Reasoning.Effort = result.Reasoning.ValidEfforts[0]
	}
	return result
}

func nullableInt(value int) *int {
	if value < 0 {
		return nil
	}
	return &value
}
