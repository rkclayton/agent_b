//go:build !windows

package updater

import (
	"context"
	"errors"
)

func verifySetupSignature(context.Context, string) error {
	return errors.New("Authenticode verification is supported only on Windows")
}

func InspectSignature(string) (Signature, error) {
	return Signature{}, errors.New("Authenticode verification is supported only on Windows")
}
