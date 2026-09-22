package modelinstall

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	contextFloor    = uint64(8192)
	contextMultiple = uint64(4096)
	contextReserve  = uint64(1 << 30)
)

// GGUFMetadata contains only the architecture values needed to size the f16
// KV cache. Keeping the parser here makes the launch decision depend on the
// verified artifact itself rather than a second, mutable catalog.
type GGUFMetadata struct {
	Architecture  string
	ContextLength uint64
	BlockCount    uint64
	HeadCountKV   uint64
	KeyLength     uint64
	ValueLength   uint64
}

type ContextSizing struct {
	NCtx            int    `json:"n_ctx"`
	WeightsBytes    int64  `json:"weights_bytes"`
	KVBytesPerToken uint64 `json:"kv_bytes_per_token"`
	AvailableBytes  uint64 `json:"available_bytes"`
	ReserveBytes    uint64 `json:"reserve_bytes"`
}

func readGGUFMetadata(path string) (GGUFMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return GGUFMetadata{}, err
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return GGUFMetadata{}, errors.New("model is not a GGUF file")
	}
	version, err := readU32(f)
	if err != nil || version < 2 || version > 3 {
		return GGUFMetadata{}, fmt.Errorf("unsupported GGUF version %d", version)
	}
	if _, err := readU64(f); err != nil {
		return GGUFMetadata{}, err
	} // tensor count
	count, err := readU64(f)
	if err != nil {
		return GGUFMetadata{}, err
	}
	if count > 1_000_000 {
		return GGUFMetadata{}, errors.New("GGUF metadata count is unreasonable")
	}
	values := map[string]any{}
	for i := uint64(0); i < count; i++ {
		key, err := readString(f)
		if err != nil {
			return GGUFMetadata{}, fmt.Errorf("read GGUF key: %w", err)
		}
		typ, err := readU32(f)
		if err != nil {
			return GGUFMetadata{}, err
		}
		value, err := readGGUFValue(f, typ, 0)
		if err != nil {
			return GGUFMetadata{}, fmt.Errorf("read GGUF value %q: %w", key, err)
		}
		values[key] = value
	}
	arch, _ := values["general.architecture"].(string)
	if arch == "" {
		return GGUFMetadata{}, errors.New("GGUF general.architecture is missing")
	}
	get := func(suffix string) uint64 { value, _ := values[arch+"."+suffix].(uint64); return value }
	metadata := GGUFMetadata{Architecture: arch, ContextLength: get("context_length"), BlockCount: get("block_count"), HeadCountKV: get("attention.head_count_kv"), KeyLength: get("attention.key_length"), ValueLength: get("attention.value_length")}
	if metadata.ContextLength == 0 || metadata.BlockCount == 0 || metadata.HeadCountKV == 0 || metadata.KeyLength == 0 || metadata.ValueLength == 0 {
		return GGUFMetadata{}, fmt.Errorf("GGUF %s context/KV metadata is incomplete", arch)
	}
	return metadata, nil
}

func computeContext(weightsBytes int64, availableBytes uint64, metadata GGUFMetadata) (ContextSizing, error) {
	kv := metadata.BlockCount * metadata.HeadCountKV * (metadata.KeyLength + metadata.ValueLength) * 2
	sizing := ContextSizing{WeightsBytes: weightsBytes, KVBytesPerToken: kv, AvailableBytes: availableBytes, ReserveBytes: contextReserve}
	if weightsBytes <= 0 || kv == 0 || availableBytes <= uint64(weightsBytes)+contextReserve {
		return sizing, fmt.Errorf("model cannot fit context floor %d: weights=%d KV-bytes/token=%d available=%d reserve=%d", contextFloor, weightsBytes, kv, availableBytes, contextReserve)
	}
	n := (availableBytes - uint64(weightsBytes) - contextReserve) / kv
	if n > metadata.ContextLength {
		n = metadata.ContextLength
	}
	n -= n % contextMultiple
	if n < contextFloor {
		return sizing, fmt.Errorf("model cannot fit context floor %d: weights=%d KV-bytes/token=%d available=%d reserve=%d", contextFloor, weightsBytes, kv, availableBytes, contextReserve)
	}
	sizing.NCtx = int(n)
	return sizing, nil
}

func readU32(r io.Reader) (uint32, error) {
	var v uint32
	err := binary.Read(r, binary.LittleEndian, &v)
	return v, err
}
func readU64(r io.Reader) (uint64, error) {
	var v uint64
	err := binary.Read(r, binary.LittleEndian, &v)
	return v, err
}
func readString(r io.Reader) (string, error) {
	n, err := readU64(r)
	if err != nil {
		return "", err
	}
	if n > 64<<20 {
		return "", errors.New("GGUF string is too large")
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return string(b), err
}
func readGGUFValue(r io.Reader, typ uint32, depth int) (any, error) {
	if depth > 2 {
		return nil, errors.New("GGUF array nesting is too deep")
	}
	switch typ {
	case 0, 1:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case 2, 3:
		var v uint16
		err := binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case 4, 5:
		var v uint32
		err := binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case 6:
		var v float32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 7:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return v != 0, err
	case 8:
		return readString(r)
	case 9:
		itemType, err := readU32(r)
		if err != nil {
			return nil, err
		}
		n, err := readU64(r)
		if err != nil {
			return nil, err
		}
		if n > 1_000_000 {
			return nil, errors.New("GGUF array is too large")
		}
		for i := uint64(0); i < n; i++ {
			if _, err := readGGUFValue(r, itemType, depth+1); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case 10, 11:
		var v uint64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 12:
		var v float64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	default:
		return nil, fmt.Errorf("unknown GGUF metadata type %d", typ)
	}
}
