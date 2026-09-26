package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Item 2lb: a verification that could not be performed is a distinct outcome
// from a signature that is bad. The updater used to decode a child's combined
// stdout and stderr as JSON, so one warning line turned a sound artifact into
// "the signature is invalid" and discarded the real reason.
var ErrVerificationUnavailable = errors.New("Authenticode verification could not be performed")

type authenticodeEvidence struct {
	Status         string    `json:"status"`
	StatusMessage  string    `json:"status_message"`
	Signer         bool      `json:"signer"`
	Timestamped    bool      `json:"timestamped"`
	Unavailable    string    `json:"unavailable"`
	SignerNotAfter time.Time `json:"-"`
	TimestampTime  time.Time `json:"-"`
}

// inspection is what a child process produced. The decision is taken from the
// report -- a payload written where only structured data can appear -- and never
// from Output, which holds whatever the two streams happened to say and exists
// only so a failure can be explained.
type inspection struct {
	Report []byte
	Output string
	Err    error
}

func readVerification(result inspection) error {
	report := bytes.TrimSpace(result.Report)
	if len(report) == 0 {
		detail := "the inspection wrote no result and said nothing"
		if output := strings.TrimSpace(result.Output); output != "" {
			detail = "the inspection said: " + firstLine(output)
		}
		if result.Err != nil {
			detail = result.Err.Error() + "; " + detail
		}
		return fmt.Errorf("%w: %s", ErrVerificationUnavailable, detail)
	}
	var evidence authenticodeEvidence
	if err := json.Unmarshal(report, &evidence); err != nil {
		return fmt.Errorf("%w: the inspection wrote a result that could not be read: %v", ErrVerificationUnavailable, err)
	}
	if strings.TrimSpace(evidence.Unavailable) != "" {
		return fmt.Errorf("%w: %s", ErrVerificationUnavailable, strings.TrimSpace(evidence.Unavailable))
	}
	return acceptAuthenticode(evidence)
}

func acceptAuthenticode(evidence authenticodeEvidence) error {
	if evidence.Status != "Valid" {
		// The tool's own words, when it gave any: "A certificate chain processed,
		// but terminated in a root certificate which is not trusted" is actionable
		// where "status is UnknownError" is not.
		if message := strings.TrimSpace(evidence.StatusMessage); message != "" {
			return fmt.Errorf("status is %s, expected Valid: %s", evidence.Status, message)
		}
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
