package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestLegacyConnectionBecomesOneModelWithoutChangingItsResolvedSettings2s5(t *testing.T) {
	raw := []byte(`{"id":"fixture","label":"Fixture","base_url":"https://models.example.test/v1","model":"first","request_timeout_s":37,"max_concurrent":3,"attachment_handling":"extract","reads_images":true,"context":{"n_ctx":64000,"reserve_output":8000},"reasoning":{"control":"top_level","enabled":false,"effort":"high"},"sampling":{"thinking":{"temperature":0.2},"nonthinking":{"temperature":0.4}},"capabilities":{"n_ctx":64000,"tool_calls":true},"measurement":{"passed":2,"total":2}}`)
	var connection Connection
	if err := json.Unmarshal(raw, &connection); err != nil {
		t.Fatal(err)
	}
	if len(connection.Models) != 1 || connection.Models[0].Model != "first" {
		t.Fatalf("models=%+v", connection.Models)
	}
	if connection.Context.NCtx != 64000 || connection.Reasoning.Effort != "high" || connection.Sampling.Nonthinking.Temperature != .4 || connection.MaxConcurrent != 3 {
		t.Fatalf("resolved=%+v", connection)
	}
	encoded, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if _, found := document["context"]; found {
		t.Fatalf("model setting remained at connection root: %s", encoded)
	}
	if _, found := document["models"]; !found {
		t.Fatalf("models missing: %s", encoded)
	}
	var reloaded Connection
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.Model != connection.Model || reloaded.Context != connection.Context || !reflect.DeepEqual(reloaded.Measurement, connection.Measurement) {
		t.Fatalf("reloaded=%+v want=%+v", reloaded, connection)
	}
}

func TestPickingASecondModelKeepsEachModelsOwnSettings2s5(t *testing.T) {
	connection := defaultConnection()
	connection.Model = "first"
	connection.Context.NCtx = 32000
	connection.Capabilities.NCtx = 32000
	connection.StoreActiveModel()
	connection.SelectModel("second", 128000)
	if connection.Context.NCtx != 128000 || connection.Context.ReserveOutput != ReserveOutputFor(128000) {
		t.Fatalf("second=%+v", connection.Context)
	}
	connection.Context.NCtx = 96000
	connection.Measurement = &Measurement{Passed: 3, Total: 3}
	connection.StoreActiveModel()
	connection.SelectModel("first", 0)
	if connection.Context.NCtx != 32000 || connection.Measurement != nil {
		t.Fatalf("first changed: context=%+v measurement=%+v", connection.Context, connection.Measurement)
	}
	connection.SelectModel("second", 0)
	if connection.Context.NCtx != 96000 || connection.Measurement == nil || connection.Measurement.Passed != 3 {
		t.Fatalf("second lost: context=%+v measurement=%+v", connection.Context, connection.Measurement)
	}
}
