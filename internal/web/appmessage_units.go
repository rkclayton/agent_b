package web

// Item 2o7: the units of `agentb-app-message-v1` (docs/app-message-v1.md), the
// plaintext inside the broker's CIPHERTEXT. The normative vectors live in
// internal/appmessage/testdata, which stays a specification (item 2ml (g)); the
// encoding here is held to their exact bytes by this package's tests.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// appUnitMax is the broker's payload ceiling, including JSON encoding.
const appUnitMax = 8 << 20

// appCanonical encodes a unit as the document requires: compact JSON with object
// keys sorted by code point, numbers carried exactly.
func appCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

type appPart struct {
	V          int    `json:"v"`
	Kind       string `json:"kind"`
	UnitID     string `json:"unit_id"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	TotalBytes int    `json:"total_bytes"`
	SHA256     string `json:"sha256"`
	Chunk      string `json:"chunk"`
}

// appSplit returns the unit itself when it fits max, and otherwise the ordered
// `part` units that carry it, each of which fits max.
func appSplit(unit []byte, max int) ([][]byte, error) {
	if len(unit) <= max {
		return [][]byte{unit}, nil
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	// The overhead of a part is measured, not guessed: an empty chunk with the
	// largest index and count this unit could need.
	overhead, err := appCanonical(appPart{V: 1, Kind: "part", UnitID: hex.EncodeToString(id), Index: len(unit), Count: len(unit), TotalBytes: len(unit), SHA256: fmt.Sprintf("%064x", 0)})
	if err != nil {
		return nil, err
	}
	chunkBytes := (max - len(overhead)) / 4 * 3
	if chunkBytes < 1 {
		return nil, fmt.Errorf("appmessage: a budget of %d bytes cannot carry a part", max)
	}
	return appSplitWith(unit, hex.EncodeToString(id), chunkBytes)
}

// appSplitWith splits with a given unit id and chunk size; the vectors fix both.
func appSplitWith(unit []byte, unitID string, chunkBytes int) ([][]byte, error) {
	count := (len(unit) + chunkBytes - 1) / chunkBytes
	digest := sha256.Sum256(unit)
	parts := make([][]byte, 0, count)
	for index := 0; index < count; index++ {
		end := min(len(unit), (index+1)*chunkBytes)
		encoded, err := appCanonical(appPart{V: 1, Kind: "part", UnitID: unitID, Index: index, Count: count, TotalBytes: len(unit), SHA256: hex.EncodeToString(digest[:]), Chunk: base64.RawURLEncoding.EncodeToString(unit[index*chunkBytes : end])})
		if err != nil {
			return nil, err
		}
		parts = append(parts, encoded)
	}
	return parts, nil
}

// appReassemble applies the receiver's rule: parts in index order, one unit, its
// length and digest checked before anything is decoded. Any mismatch is a gap.
func appReassemble(parts [][]byte) ([]byte, error) {
	var whole []byte
	var first appPart
	for index, raw := range parts {
		var p appPart
		if err := json.Unmarshal(raw, &p); err != nil || p.Kind != "part" || p.Index != index {
			return nil, errors.New("appmessage: a part is missing or out of order")
		}
		if index == 0 {
			first = p
		} else if p.UnitID != first.UnitID || p.Count != first.Count || p.SHA256 != first.SHA256 {
			return nil, errors.New("appmessage: parts of different units")
		}
		chunk, err := base64.RawURLEncoding.DecodeString(p.Chunk)
		if err != nil {
			return nil, err
		}
		whole = append(whole, chunk...)
	}
	digest := sha256.Sum256(whole)
	if len(parts) != first.Count || len(whole) != first.TotalBytes || hex.EncodeToString(digest[:]) != first.SHA256 {
		return nil, errors.New("appmessage: the reassembled unit does not match its length and digest")
	}
	return whole, nil
}
