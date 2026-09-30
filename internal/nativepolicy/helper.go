//go:build windows

package nativepolicy

type HelperRequest struct {
	Operation, Action, Account, CredentialPath, ExpectedSHA string
	Reset                                                   bool
	ACL                                                     ACLRequest
	Firewall                                                FirewallRequest
}

type HelperResult struct {
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Steps   []string `json:"steps,omitempty"`
}
