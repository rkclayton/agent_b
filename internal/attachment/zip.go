package attachment

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

type ZIPFile struct {
	Data    []byte
	Refused string
}

func ReadZIP(name string, max int64) (map[string]ZIPFile, string, error) {
	reader, err := zip.OpenReader(name)
	if err != nil {
		return nil, "", err
	}
	defer reader.Close()
	if len(reader.File) > 1000 {
		return nil, "", fmt.Errorf("zip refused: %d members exceeds 1000", len(reader.File))
	}
	result := map[string]ZIPFile{}
	lines := make([]string, 0, len(reader.File)+1)
	total := uint64(0)
	for _, file := range reader.File {
		item := ZIPFile{}
		clean := path.Clean(strings.ReplaceAll(file.Name, `\`, "/"))
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || len(clean) > 1 && clean[1] == ':' || file.Mode()&fs.ModeSymlink != 0 {
			item.Refused = "unsafe path"
		}
		total += file.UncompressedSize64
		if file.UncompressedSize64 > uint64(max) || total > uint64(max) {
			return nil, "", fmt.Errorf("zip refused: uncompressed bytes exceed %d", max)
		}
		if file.UncompressedSize64 > 0 && (file.CompressedSize64 == 0 || file.UncompressedSize64/file.CompressedSize64 > 1000) {
			return nil, "", fmt.Errorf("zip refused: %s compression ratio exceeds 1000:1", file.Name)
		}
		if item.Refused == "" && !file.FileInfo().IsDir() {
			opened, err := file.Open()
			if err != nil {
				return nil, "", err
			}
			item.Data, err = io.ReadAll(opened)
			opened.Close()
			if err != nil {
				return nil, "", err
			}
		}
		result[file.Name] = item
		suffix := strings.TrimSuffix(" refused: "+item.Refused, " refused: ")
		lines = append(lines, fmt.Sprintf("%s | %d bytes | %s%s", file.Name, file.UncompressedSize64, file.Modified.UTC().Format("2006-01-02T15:04:05Z"), suffix))
	}
	return result, strings.Join(lines, "\n") + fmt.Sprintf("\n%d members | %d bytes total", len(reader.File), total), nil
}
