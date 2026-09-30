//go:build !windows

package ocr

import (
	"context"
	"fmt"
)

func Extract(string) (string, error) {
	return "", fmt.Errorf("Windows.Media.Ocr is available only on Windows")
}

func ExtractPDF(context.Context, string, func(int, int)) (string, int, int, bool, error) {
	return "", 0, 0, false, fmt.Errorf("Windows.Data.Pdf is available only on Windows")
}
