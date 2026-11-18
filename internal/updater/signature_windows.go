//go:build windows

package updater

import (
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	signatureCrypt32      = windows.NewLazySystemDLL("crypt32.dll")
	signatureMessageParam = signatureCrypt32.NewProc("CryptMsgGetParam")
	signatureMessageClose = signatureCrypt32.NewProc("CryptMsgClose")
)

func verifySetupSignature(ctx context.Context, path string) error {
	return readVerification(inspectAuthenticode(ctx, path))
}

// inspectAuthenticode keeps the structured decision separate from an inspection error.
func inspectAuthenticode(ctx context.Context, path string) inspection {
	if err := ctx.Err(); err != nil {
		return inspection{Err: err}
	}
	signature, err := InspectSignature(path)
	if err != nil {
		return inspection{Err: err}
	}
	report, err := json.Marshal(authenticodeEvidence{Status: signature.Status, Subject: signature.Subject, Signer: signature.Thumbprint != ""})
	return inspection{Report: report, Err: err}
}

// InspectSignature uses the same Windows trust provider as Authenticode without a shell.
func InspectSignature(path string) (Signature, error) {
	result := Signature{Status: "NotSigned", Subject: "none"}
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return result, err
	}
	if _, err = os.Stat(path); err != nil {
		return result, err
	}
	info := windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: wide}
	trust := windows.WinTrustData{Size: uint32(unsafe.Sizeof(windows.WinTrustData{})), UIChoice: windows.WTD_UI_NONE, UnionChoice: windows.WTD_CHOICE_FILE, StateAction: windows.WTD_STATEACTION_VERIFY, FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&info)}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &trust)
	defer func() {
		trust.StateAction = windows.WTD_STATEACTION_CLOSE
		_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &trust)
	}()
	switch verifyErr {
	case nil:
		result.Status = "Valid"
	case windows.Errno(0x80096010):
		result.Status = "HashMismatch"
	case windows.Errno(0x800b0100), windows.Errno(0x800b0003):
		return result, nil
	default:
		result.Status = "NotTrusted"
	}
	var store, message windows.Handle
	if err := windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(wide), windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY, 0, nil, nil, nil, &store, &message, nil); err != nil {
		return result, err
	}
	defer windows.CertCloseStore(store, 0)
	defer signatureMessageClose.Call(uintptr(message))
	var size uint32
	get := signatureMessageParam
	if ok, _, err := get.Call(uintptr(message), 7, 0, 0, uintptr(unsafe.Pointer(&size))); ok == 0 {
		return result, err
	}
	if size < uint32(unsafe.Sizeof(windows.CertInfo{})) || size > 64<<10 {
		return result, fmt.Errorf("signer certificate information size is invalid")
	}
	buffer := make([]byte, size)
	if ok, _, err := get.Call(uintptr(message), 7, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size))); ok == 0 {
		return result, err
	}
	cert, err := windows.CertFindCertificateInStore(store, windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, 0, windows.CERT_FIND_SUBJECT_CERT, unsafe.Pointer(&buffer[0]), nil)
	if err != nil {
		return result, err
	}
	defer windows.CertFreeCertificateContext(cert)
	if cert.Length > 64<<10 {
		return result, fmt.Errorf("signer certificate size is invalid")
	}
	encoded := unsafe.Slice(cert.EncodedCert, cert.Length)
	parsed, err := x509.ParseCertificate(encoded)
	if err != nil {
		return result, err
	}
	result.Subject = parsed.Subject.String()
	result.Thumbprint = fmt.Sprintf("%X", sha1.Sum(encoded))
	return result, nil
}
