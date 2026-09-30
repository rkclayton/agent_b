# Organisation signing

An organisation can stamp a staged Agent_b release with its own Windows code-signing certificate without changing the embedded payload. This lets publisher policy trust the installer and the installed `Agent_b.exe`, which are the same signed executable. The bundled `agentb.exe`, runtime scripts, payload digest, and Microsoft-signed WebView2 loader remain the release payload and retain their original signatures.

Run Windows PowerShell non-interactively from the repository root after staging and before publishing `release.json`:

```powershell
powershell.exe -NoLogo -NoProfile -NonInteractive -File tools\sign-release.ps1 `
  -Organization -Path C:\staging\Agent_b-v1.51.0 `
  -Thumbprint YOUR_CERTIFICATE_THUMBPRINT `
  -TimestampUrl https://timestamp.example.org
```

The certificate may be in `LocalMachine\My` or `CurrentUser\My`. For a PFX, place its password in a private environment variable and name that variable; the password is never an argument or manifest field:

```powershell
$env:ORG_PFX_PASSWORD = '<supplied by your CI secret store>'
powershell.exe -NoLogo -NoProfile -NonInteractive -File tools\sign-release.ps1 `
  -Organization -Path C:\staging\Agent_b-v1.51.0 `
  -PfxPath C:\private\publisher.pfx -PfxPasswordEnvironment ORG_PFX_PASSWORD `
  -TimestampUrl https://timestamp.example.org
Remove-Item Env:\ORG_PFX_PASSWORD
```

The tool signs every Agent_b PE present in the staged set (`Agent_b-setup.exe`, `Agent_b.exe`, and `agentb.exe`), requires a timestamp, updates `candidate-final.json`, and writes `org-signing-manifest.json` with each file's digest, signer subject, thumbprint, and timestamp authority. Publish a new `release.json` from those final bytes. Do not re-sign `WebView2Loader.dll`: it is pinned and remains Microsoft-signed.

Publisher policy should trust the organisation certificate thumbprint recorded in the manifest and the Microsoft publisher used by WebView2. The installer log independently names the outer signer and the still-verified embedded payload signer. Re-signing does not weaken the bundle SHA-256 check, the release-manifest digest check, or Authenticode verification; modifying the embedded payload after signing is refused.

By default updates still use Agent_b's GitHub releases. An organisation hosting re-signed releases can set the process environment variable `AGENTB_UPDATE_SOURCE_URL` to its HTTPS latest-release endpoint. That endpoint uses the same `tag_name` and assets shape as GitHub's latest-release response and provides `release.json` plus `Agent_b-setup.exe`. HTTPS is mandatory, asset URLs may not downgrade it, and the manifest identity, exact size, SHA-256, executable identity, and outer Authenticode signature are checked exactly as on the default source.

Connections use Go's standard Windows root pool. A server certificate chaining to a CA installed in the Windows machine trust store is therefore accepted; an untrusted chain is refused as `x509: certificate signed by unknown authority`. The phone uses its platform system trust store independently; the Mac client repository must test that platform behavior.
