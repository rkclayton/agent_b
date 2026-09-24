package updater

import (
	"errors"
	"fmt"
	"time"
)

type authenticodeEvidence struct {
	Status         string    `json:"status"`
	Signer         bool      `json:"signer"`
	Timestamped    bool      `json:"timestamped"`
	SignerNotAfter time.Time `json:"-"`
	TimestampTime  time.Time `json:"-"`
}

func acceptAuthenticode(evidence authenticodeEvidence) error {
	if evidence.Status != "Valid" {
		return fmt.Errorf("status is %s, expected Valid", evidence.Status)
	}
	if !evidence.Signer {
		return errors.New("signer certificate is missing")
	}
	if !evidence.Timestamped {
		return errors.New("trusted timestamp is missing")
	}
	// Windows incorporates the timestamp into Status. Do not compare NotAfter
	// with today: doing so would incorrectly expire a signature that was valid
	// when its trusted timestamp was applied.
	return nil
}
