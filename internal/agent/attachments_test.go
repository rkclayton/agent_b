package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/llm"
	"harness/internal/session"
	"harness/internal/tools"
)

func smallPNG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestImageAccountingUsesBoundedPixelsByProvider2v5(t *testing.T) {
	for _, row := range []struct {
		protocol string
		width    int
		height   int
		want     int64
	}{
		{config.ProtocolAnthropicMessages, 1568, 522, 1064},
		{config.ProtocolOpenAIResponses, 1568, 522, 833},
		{config.ProtocolChatCompletions, 1568, 522, 833},
		{config.ProtocolChatCompletions, 400, 300, 130},
	} {
		connection := config.Defaults(t.TempDir()).Connections[0]
		connection.Protocol = row.protocol
		if got := nativeImageTokens(&connection, row.width, row.height); got != row.want {
			t.Errorf("%s %dx%d tokens=%d want=%d", row.protocol, row.width, row.height, got, row.want)
		}
	}
}

func TestWebPInsideAndOverBoundFollowTheImagePath2v5(t *testing.T) {
	inside, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}
	got, mime, err := boundedModelImage(inside, "image/webp")
	if err != nil || mime != "image/webp" || !bytes.Equal(got, inside) {
		t.Fatalf("inside webp: bytes=%d mime=%q err=%v", len(got), mime, err)
	}
	over, err := base64.StdEncoding.DecodeString("UklGRmACAABXRUJQVlA4WAoAAAAgAAAAzwcAAAAASUNDUMgBAAAAAAHIAAAAAAQwAABtbnRyUkdCIFhZWiAH4AABAAEAAAAAAABhY3NwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAA9tYAAQAAAADTLQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAlkZXNjAAAA8AAAACRyWFlaAAABFAAAABRnWFlaAAABKAAAABRiWFlaAAABPAAAABR3dHB0AAABUAAAABRyVFJDAAABZAAAAChnVFJDAAABZAAAAChiVFJDAAABZAAAAChjcHJ0AAABjAAAADxtbHVjAAAAAAAAAAEAAAAMZW5VUwAAAAgAAAAcAHMAUgBHAEJYWVogAAAAAAAAb6IAADj1AAADkFhZWiAAAAAAAABimQAAt4UAABjaWFlaIAAAAAAAACSgAAAPhAAAts9YWVogAAAAAAAA9tYAAQAAAADTLXBhcmEAAAAAAAQAAAACZmYAAPKnAAANWQAAE9AAAApbAAAAAAAAAABtbHVjAAAAAAAAAAEAAAAMZW5VUwAAACAAAAAcAEcAbwBvAGcAbABlACAASQBuAGMALgAgADIAMAAxADZWUDggcgAAADAKAJ0BKtAHAQA+USiSRyOioaEgSABwCglpbuF2AR7cAAAnteirhBkENVSa7bRcIMghqqTXbaLhBkENVSa7bRcIMghqqTXbaLhBkENVSa7bRcIMghqqTXaYAAD+/58D//5JHq5joP/7oMs4ZzAAAAAAAA==")
	if err != nil {
		t.Fatal(err)
	}
	got, mime, err = boundedModelImage(over, "image/webp")
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(got))
	if err != nil || decoded.Bounds().Dx() != modelImageMaxDimension || decoded.Bounds().Dy() != 1 || mime == "image/webp" {
		t.Fatalf("bounded webp: bounds=%v mime=%q bytes=%d err=%v", decoded.Bounds(), mime, len(got), err)
	}
}

func TestEveryImageSourceIsBoundedWithoutChangingStoredBytes2qu(t *testing.T) {
	for _, relative := range []string{"attachments/large.png", "generated/large.png", "screenshots/large.png"} {
		t.Run(relative, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			picture := image.NewNRGBA(image.Rect(0, 0, 3000, 1000))
			for y := range 1000 {
				for x := range 3000 {
					picture.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
				}
			}
			var stored bytes.Buffer
			if err := png.Encode(&stored, picture); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, stored.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			connection := config.Defaults(workspace).Connections[0]
			connection.ReadsImages, connection.Capabilities.ImageInput, connection.Capabilities.Vision = true, true, config.VisionReadsImages
			message := events.Message{Role: "user", Attachments: []events.Attachment{{Path: relative, Bytes: int64(stored.Len())}}}
			parts := requestMessage(&connection, &session.Session{Workspace: workspace}, message).Content.([]any)
			payload := parts[len(parts)-1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
			encoded, _ := strings.CutPrefix(payload, "data:image/png;base64,")
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			bounded, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if bounded.Bounds().Dx() > modelImageMaxDimension || bounded.Bounds().Dy() > modelImageMaxDimension {
				t.Fatalf("request dimensions=%v", bounded.Bounds())
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, stored.Bytes()) {
				t.Fatal("stored image changed")
			}
			cost := nativeAttachmentCost(&connection, workspace, message.Attachments[0])
			if cost != 833 {
				t.Fatalf("bounded pixel cost=%d want=833", cost)
			}
			kept := prepareNativeAttachmentsWithBudget(&connection, workspace, message.Attachments, 1000)
			if kept[0].Outcome != "" {
				t.Fatalf("pixel-accounted image was dropped: %q", kept[0].Outcome)
			}
			t.Logf("old byte estimates before=%d after=%d; bounded pixel estimate=%d; request bytes=%d mime=%s", (stored.Len()+2)/3, 6545, cost, len(data), strings.TrimPrefix(strings.Split(payload, ";")[0], "data:"))
		})
	}
}

func TestScaledImageUsesTheSmallerEncoding2v5(t *testing.T) {
	picture := image.NewNRGBA(image.Rect(0, 0, 3000, 1000))
	for y := range 1000 {
		for x := range 3000 {
			picture.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, picture); err != nil {
		t.Fatal(err)
	}
	selected, mime, err := boundedModelImage(source.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(selected))
	if err != nil {
		t.Fatal(err)
	}
	var other bytes.Buffer
	if mime == "image/jpeg" {
		err = png.Encode(&other, decoded)
	} else {
		err = jpeg.Encode(&other, decoded, &jpeg.Options{Quality: 85})
	}
	if err != nil || len(selected) > other.Len() {
		t.Fatalf("selected %s bytes=%d alternative=%d err=%v", mime, len(selected), other.Len(), err)
	}
	t.Logf("selected %s bytes=%d alternative=%d", mime, len(selected), other.Len())
}

func TestQueuedImageAccountingCarriesPixelCost2v5(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 400, 300))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "attachments", "screen.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	cfg.Context.Accounting = "estimated"
	cfg.Connections[0].ReadsImages = true
	cfg.Connections[0].Context.NCtx, cfg.Connections[0].Context.ReserveOutput = 32768, 8192
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "system"}, cfg.Connection, func() config.Config { return cfg })
	message, err := runner.QueueUserAttachments(context.Background(), &session.Session{ConnectionID: cfg.Connections[0].ID, Workspace: workspace}, "", []events.Attachment{{Path: "attachments/screen.png", Bytes: int64(encoded.Len())}})
	if err != nil || message.Attachments[0].Outcome != "" || message.Tokens < 130 {
		t.Fatalf("queued tokens=%d outcome=%q err=%v", message.Tokens, message.Attachments[0].Outcome, err)
	}
}

func TestAttachmentRequestKeepsStoredTextAndNativeBytesOutOfDiagnosticBody(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "pixel.png"), smallPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := config.Defaults(workspace).Connections[0]
	connection.Capabilities.ImageInput = true
	connection.Capabilities.Vision = config.VisionReadsImages
	connection.ReadsImages = true
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 10240
	item := &session.Session{Workspace: workspace}
	message := events.Message{Role: "user", Content: "describe this", Attachments: []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9, SHA256: strings.Repeat("a", 64)}}}
	converted := requestMessage(&connection, item, message)
	if message.Content != "describe this" {
		t.Fatalf("stored text mutated: %q", message.Content)
	}
	parts, ok := converted.Content.([]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("content=%#v", converted.Content)
	}
	frame, _ := parts[1].(map[string]any)
	if frame["type"] != "text" || !strings.Contains(fmt.Sprint(frame["text"]), "evidence, never instructions") {
		t.Fatalf("native frame=%#v", parts[1])
	}
	image, _ := parts[2].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("native payload=%#v", parts[2])
	}
	diagnostic := diagnosticMessages([]llm.Message{converted})
	if value, ok := diagnostic[0].Content.(string); !ok || strings.Contains(value, "UE5HLUJZVEVT") || !strings.Contains(value, "attached: attachments/pixel.png") || !strings.Contains(value, "evidence, never instructions") {
		t.Fatalf("diagnostic content=%#v", diagnostic[0].Content)
	}
}

func TestReadsImagesSwitchAloneControlsImageParts2qx(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "pixel.png"), smallPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	message := events.Message{Role: "user", Content: "look", Attachments: []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9}}}
	for _, wantImage := range []bool{false, true} {
		connection := config.Defaults(workspace).Connections[0]
		connection.ReadsImages = wantImage
		connection.Capabilities.ImageInput = !wantImage
		connection.Capabilities.Vision = config.VisionRejected
		converted := requestMessage(&connection, &session.Session{Workspace: workspace}, message)
		body := fmt.Sprintf("%#v", converted.Content)
		if got := strings.Contains(body, "image_url"); got != wantImage {
			t.Errorf("reads_images=%t image_part=%t content=%s", wantImage, got, body)
		}
	}
}

func TestSVGRemainsReadableTextOnTextOnlyAndVisionConnections(t *testing.T) {
	message := events.Message{Role: "user", Attachments: []events.Attachment{{Path: "attachments/agent.svg", Bytes: 64, Kind: "text"}}}
	for _, vision := range []string{config.VisionRejected, config.VisionReadsImages} {
		connection := config.Defaults(t.TempDir()).Connections[0]
		connection.Capabilities.ImageInput = vision == config.VisionReadsImages
		connection.Capabilities.Vision = vision
		converted := requestMessage(&connection, &session.Session{}, message)
		text, ok := converted.Content.(string)
		if !ok || !strings.Contains(text, "read it with read_file") || strings.Contains(text, "binary") {
			t.Fatalf("vision=%q content=%#v", vision, converted.Content)
		}
	}
}

func TestAttachmentNativeOverrideSendsImageWhenProbeSaysAbsent(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "pixel.png"), smallPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := config.Defaults(workspace).Connections[0]
	connection.Capabilities.ImageInput = false
	connection.AttachmentHandling = "native"
	connection.ReadsImages = true
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 10240
	message := events.Message{Role: "user", Attachments: []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9}}}
	converted := requestMessage(&connection, &session.Session{Workspace: workspace}, message)
	parts, ok := converted.Content.([]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("native override did not send image: %#v", converted.Content)
	}
}

func TestNativeAttachmentsEachHaveAnImmediatelyAdjacentFrame(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.png", "second.jpg"} {
		if err := os.WriteFile(filepath.Join(workspace, "attachments", name), smallPNG(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	connection := config.Defaults(workspace).Connections[0]
	connection.Capabilities.ImageInput = true
	connection.Capabilities.Vision = config.VisionReadsImages
	connection.ReadsImages = true
	connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 10240
	message := events.Message{Role: "user", Content: "compare", Attachments: []events.Attachment{
		{Path: "attachments/first.png", Bytes: 9},
		{Path: "attachments/second.jpg", Bytes: 10},
	}}
	converted := requestMessage(&connection, &session.Session{Workspace: workspace}, message)
	parts, ok := converted.Content.([]any)
	if !ok || len(parts) != 5 {
		t.Fatalf("content=%#v", converted.Content)
	}
	for index, path := range []string{"attachments/first.png", "attachments/second.jpg"} {
		frame, _ := parts[1+index*2].(map[string]any)
		payload, _ := parts[2+index*2].(map[string]any)
		if frame["type"] != "text" || !strings.Contains(fmt.Sprint(frame["text"]), path) || payload["type"] != "image_url" {
			t.Fatalf("pair %d: frame=%#v payload=%#v", index, frame, payload)
		}
	}
}

func TestNativeAttachmentOverContextBudgetHasVisibleOutcomeAndNoPayload(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "pixel.png"), smallPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := config.Defaults(workspace).Connections[0]
	connection.Capabilities.ImageInput = true
	connection.Capabilities.Vision = config.VisionReadsImages
	connection.ReadsImages = true
	connection.Context.NCtx, connection.Context.ReserveOutput = 8, 8
	attachments := prepareNativeAttachments(&connection, workspace, []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9}})
	if !strings.Contains(attachments[0].Outcome, "not sent inline") || !strings.Contains(attachments[0].Outcome, "only 0 remain") {
		t.Fatalf("outcome=%q", attachments[0].Outcome)
	}
	converted := requestMessage(&connection, &session.Session{Workspace: workspace}, events.Message{Role: "user", Attachments: attachments})
	text, ok := converted.Content.(string)
	if !ok || !strings.Contains(text, attachments[0].Outcome) || strings.Contains(text, "read it with read_file") {
		t.Fatalf("content=%#v", converted.Content)
	}
	cfg := config.Defaults(workspace)
	cfg.Connections[0] = connection
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "system"}, cfg.Connection, func() config.Config { return cfg })
	queued, err := runner.QueueUserAttachments(context.Background(), &session.Session{ConnectionID: connection.ID, Workspace: workspace}, "", []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9}})
	if err != nil || len(queued.Attachments) != 1 || queued.Attachments[0].Outcome != attachments[0].Outcome {
		t.Fatalf("queued=%#v err=%v", queued, err)
	}
}

func TestNativeAttachmentBudgetIsCumulativeAcrossQueuedHistory(t *testing.T) {
	workspace := t.TempDir()
	connection := config.Defaults(workspace).Connections[0]
	connection.Capabilities.ImageInput = true
	connection.Capabilities.Vision = config.VisionReadsImages
	connection.ReadsImages = true
	connection.Context.NCtx, connection.Context.ReserveOutput = 100, 8
	cfg := config.Defaults(workspace)
	cfg.Connections[0] = connection
	runner := NewRunner(events.NewBus(), tools.New(), &PromptRenderer{text: "system"}, cfg.Connection, func() config.Config { return cfg })
	item := &session.Session{ConnectionID: connection.ID, Workspace: workspace}
	attachment := events.Attachment{Path: "attachments/pixel.png", Bytes: 9}
	for index := 0; index < 2; index++ {
		message, err := runner.QueueUserAttachments(context.Background(), item, "", []events.Attachment{attachment})
		if err != nil || message.Attachments[0].Outcome != "" {
			t.Fatalf("queue %d: message=%#v err=%v", index, message, err)
		}
		item.Append(message)
	}
	message, err := runner.QueueUserAttachments(context.Background(), item, "", []events.Attachment{attachment})
	if err != nil || !strings.Contains(message.Attachments[0].Outcome, "not sent inline") {
		t.Fatalf("third queue: message=%#v err=%v", message, err)
	}
}

func TestAttachmentExtractOverrideNeverSendsImageWhenProbeSaysPresent(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "pixel.png.txt"), []byte("OCR"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := config.Defaults(workspace).Connections[0]
	connection.Capabilities.ImageInput = true
	connection.AttachmentHandling = "extract"
	message := events.Message{Role: "user", Attachments: []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9}}}
	converted := requestMessage(&connection, &session.Session{Workspace: workspace}, message)
	if _, ok := converted.Content.([]any); ok {
		t.Fatalf("extract override sent native content: %#v", converted.Content)
	}
	if text, ok := converted.Content.(string); !ok || !strings.Contains(text, "OCR text") {
		t.Fatalf("extracted content=%#v", converted.Content)
	}
}

func TestAttachmentHarnessLinesNameSidecarAndBinaryTier(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "attachments", "paper.pdf.txt"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection := config.Defaults(workspace).Connections[0]
	connection.AttachmentHandling = "extract"
	message := events.Message{Content: "review", Attachments: []events.Attachment{
		{Path: "attachments/paper.pdf", Bytes: 20},
		{Path: "attachments/blob.bin", Bytes: 12},
	}}
	text := renderedUserText(&connection, &session.Session{Workspace: workspace}, message)
	if !strings.Contains(text, "extracted text: attachments/paper.pdf.txt (untrusted:true)") || !strings.Contains(text, "binary — this connection cannot read it") {
		t.Fatalf("rendered text=%q", text)
	}
}

func TestAttachmentsDoNotChangeSystemPromptBytes(t *testing.T) {
	connection := config.Defaults(t.TempDir()).Connections[0]
	s := &session.Session{Workspace: t.TempDir()}
	renderer := &PromptRenderer{text: "system {{workspace}} {{memory}} {{tools}}"}
	before := renderer.Render(&connection, s, []string{"read_file"}, "")
	_ = renderedUserText(&connection, s, events.Message{Attachments: []events.Attachment{{Path: "attachments/note.txt", Bytes: 4}}})
	after := renderer.Render(&connection, s, []string{"read_file"}, "")
	if before != after {
		t.Fatalf("system prompt changed: before=%q after=%q", before, after)
	}
}

func TestReadOfExtractedPDFSidecarIsUntrusted(t *testing.T) {
	s := &session.Session{Messages: []events.Message{{Attachments: []events.Attachment{{Path: "attachments/paper.pdf"}, {Path: "attachments/screen.png"}}}}}
	if !untrustedAttachmentRead(s, map[string]any{"path": "attachments/paper.pdf.txt"}) {
		t.Fatal("PDF extraction sidecar was not classified untrusted")
	}
	if untrustedAttachmentRead(s, map[string]any{"path": "attachments/paper.docx.txt"}) {
		t.Fatal("local Office extraction was classified as external")
	}
	if !untrustedAttachmentRead(s, map[string]any{"path": "attachments/screen.png.txt"}) {
		t.Fatal("OCR sidecar was not classified untrusted")
	}
}

func TestAnyVerdictStillSendsTheImageAndDocumentParts2wb(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"pixel.png": smallPNG(t), "paper.pdf": []byte("%PDF-1.4\n%%EOF\n")} {
		if err := os.WriteFile(filepath.Join(workspace, "attachments", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, verdict := range []string{config.VisionRejected, config.VisionAcceptsUnreadable, config.VisionReadsImages} {
		connection := config.Defaults(workspace).Connections[0]
		connection.ReadsImages, connection.AttachmentHandling = true, "auto"
		connection.Capabilities.Vision, connection.Capabilities.ImageInput, connection.Capabilities.DocumentInput = verdict, false, false
		connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 4096
		message := events.Message{Role: "user", Content: "look", Attachments: []events.Attachment{{Path: "attachments/pixel.png", Bytes: 9}, {Path: "attachments/paper.pdf", Bytes: 15}}}
		body := fmt.Sprintf("%#v", requestMessage(&connection, &session.Session{Workspace: workspace}, message).Content)
		if !strings.Contains(body, "image_url") || !strings.Contains(body, "file_data") {
			t.Fatalf("verdict %q did not send both parts: %s", verdict, body)
		}
	}
}

func TestARefusedAttachmentPartIsRetriedOnceAsText2wb(t *testing.T) {
	for _, tc := range []struct {
		name, file, part, kind string
		refuse                 bool
	}{
		{"image refused", "pixel.png", "image_url", "image", true},
		{"document refused", "paper.pdf", "file_data", "document", true},
		{"image accepted", "pixel.png", "image_url", "image", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				raw, _ := io.ReadAll(request.Body)
				bodies = append(bodies, string(raw))
				if tc.refuse && strings.Contains(string(raw), tc.part) {
					http.Error(w, `{"error":{"message":"content part is not supported"}}`, http.StatusBadRequest)
					return
				}
				writeStreamChunk(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "described"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 50, "completion_tokens": 1}})
			}))
			defer server.Close()
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "attachments"), 0o700); err != nil {
				t.Fatal(err)
			}
			content := map[string][]byte{"pixel.png": smallPNG(t), "paper.pdf": []byte("%PDF-1.4\n%%EOF\n")}[tc.file]
			if err := os.WriteFile(filepath.Join(root, "attachments", tc.file), content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "attachments", tc.file+".txt"), []byte("THE TEXT"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults(root)
			cfg.Context.Accounting = "estimated"
			connection := cfg.Connections[0]
			connection.ID, connection.BaseURL, connection.Model = "fixture", server.URL, "fixture"
			connection.ReadsImages, connection.AttachmentHandling = true, "auto"
			connection.Context.NCtx, connection.Context.ReserveOutput = 32768, 4096
			cfg.Connections = []config.Connection{connection}
			bus := newCapturedBus()
			runner := NewRunner(bus.Bus, tools.New(), &PromptRenderer{text: "system"}, func(string) (*config.Connection, bool) { return &connection, true }, func() config.Config { return cfg })
			item := &session.Session{ID: "chat", ConnectionID: connection.ID, Workspace: root, Runnable: true, ToolsEnabled: map[string]bool{}, ToolCalls: map[string]int{}, SchemaTokens: map[string]int{}, MarginalTokens: map[string]int{}}
			item.Append(events.Message{ID: "task", Role: "user", Category: "history", Content: "what is this", Attachments: []events.Attachment{{Path: "attachments/" + tc.file, Bytes: int64(len(content))}}})
			if reason, detail, _ := runner.Run(t.Context(), item, "run"); reason != "done" {
				t.Fatalf("reason=%s detail=%q", reason, detail)
			}
			lines := []string{}
			for _, event := range bus.Recent(item.ID) {
				if data, ok := event.Data.(map[string]any); ok && event.Type == events.ModelRetry && data["reason"] == "attachment_as_text" {
					lines = append(lines, fmt.Sprint(data["kind"]))
				}
			}
			if !tc.refuse {
				if len(bodies) != 1 || len(lines) != 0 {
					t.Fatalf("accepted: calls=%d lines=%v", len(bodies), lines)
				}
				return
			}
			if len(bodies) != 2 || strings.Contains(bodies[1], tc.part) || !strings.Contains(bodies[1], tc.file+".txt") || len(lines) != 1 || lines[0] != tc.kind {
				t.Fatalf("refused: calls=%d lines=%v second=%s", len(bodies), lines, bodies[len(bodies)-1])
			}
		})
	}
}
