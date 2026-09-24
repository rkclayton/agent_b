//go:build windows

package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func verifySetupSignature(ctx context.Context, path string) error {
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	quoted := strings.ReplaceAll(path, "'", "''")
	script := "$s=Get-AuthenticodeSignature -LiteralPath '" + quoted + "'; [pscustomobject]@{status=[string]$s.Status;signer=[bool]$s.SignerCertificate;timestamped=[bool]$s.TimeStamperCertificate}|ConvertTo-Json -Compress"
	output, err := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect signature: %s", strings.TrimSpace(string(output)))
	}
	var evidence authenticodeEvidence
	if err := json.Unmarshal(output, &evidence); err != nil {
		return fmt.Errorf("decode signature status: %w", err)
	}
	return acceptAuthenticode(evidence)
}
