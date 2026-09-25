# Item 2hj (v1.5.1): inspect CNG key UI policy without acquiring a private
# signing handle. NCRYPT_SILENT_FLAG turns a key that would show Windows UI
# into NTE_SILENT_CONTEXT, which is a refusal signal rather than a dialog.

if (-not ('AgentBSigningKeyPolicy' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Cryptography.X509Certificates;

public static class AgentBSigningKeyPolicy
{
    const int CERT_KEY_PROV_INFO_PROP_ID = 2;
    const int NCRYPT_MACHINE_KEY_FLAG = 0x20;
    const int NCRYPT_SILENT_FLAG = 0x40;
    const int NTE_BAD_KEYSET = unchecked((int)0x80090016);
    const int NTE_NOT_FOUND = unchecked((int)0x80090011);
    const int NTE_SILENT_CONTEXT = unchecked((int)0x80090022);

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    struct CRYPT_KEY_PROV_INFO
    {
        public IntPtr pwszContainerName;
        public IntPtr pwszProvName;
        public uint dwProvType;
        public uint dwFlags;
        public uint cProvParam;
        public IntPtr rgProvParam;
        public uint dwKeySpec;
    }

    [DllImport("crypt32.dll", SetLastError = true)]
    static extern bool CertGetCertificateContextProperty(IntPtr context, int propertyId, IntPtr data, ref int size);
    [DllImport("ncrypt.dll", CharSet = CharSet.Unicode)]
    static extern int NCryptOpenStorageProvider(out IntPtr provider, string name, int flags);
    [DllImport("ncrypt.dll", CharSet = CharSet.Unicode)]
    static extern int NCryptOpenKey(IntPtr provider, out IntPtr key, string name, int legacyKeySpec, int flags);
    [DllImport("ncrypt.dll", CharSet = CharSet.Unicode)]
    static extern int NCryptGetProperty(IntPtr obj, string property, byte[] output, int outputLength, out int resultLength, int flags);
    [DllImport("ncrypt.dll")]
    static extern int NCryptFreeObject(IntPtr obj);

    static string Hex(int status) { return "0x" + status.ToString("X8"); }

    // state, flags, provider, container, detail
    public static string[] Read(X509Certificate2 certificate, bool machineStore)
    {
        int size = 0;
        if (!CertGetCertificateContextProperty(certificate.Handle, CERT_KEY_PROV_INFO_PROP_ID, IntPtr.Zero, ref size))
            return new [] { "unreadable", "", "", "", new Win32Exception(Marshal.GetLastWin32Error()).Message };
        IntPtr memory = Marshal.AllocHGlobal(size);
        IntPtr provider = IntPtr.Zero;
        IntPtr key = IntPtr.Zero;
        try
        {
            if (!CertGetCertificateContextProperty(certificate.Handle, CERT_KEY_PROV_INFO_PROP_ID, memory, ref size))
                return new [] { "unreadable", "", "", "", new Win32Exception(Marshal.GetLastWin32Error()).Message };
            var info = (CRYPT_KEY_PROV_INFO)Marshal.PtrToStructure(memory, typeof(CRYPT_KEY_PROV_INFO));
            string container = Marshal.PtrToStringUni(info.pwszContainerName) ?? "";
            string providerName = Marshal.PtrToStringUni(info.pwszProvName) ?? "";
            if (info.dwProvType != 0) return new [] { "legacy-provider", "", providerName, container, "CNG UI policy is unavailable" };
            int status = NCryptOpenStorageProvider(out provider, providerName, 0);
            if (status != 0) return new [] { "unreadable", "", providerName, container, Hex(status) };
            int openFlags = NCRYPT_SILENT_FLAG | (machineStore ? NCRYPT_MACHINE_KEY_FLAG : 0);
            status = NCryptOpenKey(provider, out key, container, 0, openFlags);
            if (status == NTE_SILENT_CONTEXT) return new [] { "prompting", "", providerName, container, Hex(status) };
            if (status == NTE_BAD_KEYSET && machineStore) return new [] { "elevation-required", "", providerName, container, Hex(status) };
            if (status != 0) return new [] { "unreadable", "", providerName, container, Hex(status) };
            int bytes;
            status = NCryptGetProperty(key, "UI Policy", null, 0, out bytes, NCRYPT_SILENT_FLAG);
            if (status == NTE_NOT_FOUND) return new [] { "none", "0", providerName, container, "" };
            if (status != 0) return new [] { "unreadable", "", providerName, container, Hex(status) };
            byte[] value = new byte[bytes];
            status = NCryptGetProperty(key, "UI Policy", value, value.Length, out bytes, NCRYPT_SILENT_FLAG);
            if (status != 0) return new [] { "unreadable", "", providerName, container, Hex(status) };
            uint flags = value.Length >= 8 ? BitConverter.ToUInt32(value, 4) : 0;
            return new [] { flags == 0 ? "none" : "prompting", flags.ToString(), providerName, container, "" };
        }
        finally
        {
            if (key != IntPtr.Zero) NCryptFreeObject(key);
            if (provider != IntPtr.Zero) NCryptFreeObject(provider);
            Marshal.FreeHGlobal(memory);
        }
    }
}
'@
}

function Get-SigningKeyUIPolicy {
    param(
        [Parameter(Mandatory = $true)]$Certificate,
        [Parameter(Mandatory = $true)][string]$Store
    )
    $values = [AgentBSigningKeyPolicy]::Read($Certificate, ($Store -match '(?i)LocalMachine'))
    return [pscustomobject]@{
        State = $values[0]
        Flags = $values[1]
        Provider = $values[2]
        Container = $values[3]
        Detail = $values[4]
    }
}

function Assert-SigningKeyPolicy {
    param(
        [Parameter(Mandatory = $true)][string]$Thumbprint,
        [Parameter(Mandatory = $true)][string]$Store,
        [Parameter(Mandatory = $true)]$Policy
    )
    if ($Policy.State -in @('none', 'elevation-required')) { return $Policy }
    throw "SIGNING KEY REFUSED: certificate $Thumbprint in $Store may require interactive private-key UI; no private key was opened."
}

function Assert-SigningKeyNonInteractive {
    param(
        [Parameter(Mandatory = $true)]$Certificate,
        [Parameter(Mandatory = $true)][string]$Store
    )
    $policy = Get-SigningKeyUIPolicy -Certificate $Certificate -Store $Store
    return Assert-SigningKeyPolicy -Thumbprint $Certificate.Thumbprint -Store $Store -Policy $policy
}

# 2ki: one policy names the runtime payload files Authenticode cannot sign.
# Every other runtime-scripts.txt entry must be a signable PowerShell file.
function Get-AgentBRuntimeSigningPolicy {
    param([Parameter(Mandatory = $true)][string]$Root)
    $manifest = Join-Path $Root 'runtime-scripts.txt'
    if (-not (Test-Path -LiteralPath $manifest -PathType Leaf)) { throw 'runtime-scripts.txt is missing.' }
    $unsigned = @('scripts/launch-hidden.vbs', 'scripts/launch-installed.cmd', 'scripts/webview2-loader.json')
    $entries = @(Get-Content -LiteralPath $manifest | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    $unexpected = @($entries | Where-Object { $_ -notmatch '\.(?:ps1|psm1)$' -and $_ -notin $unsigned })
    $missing = @($unsigned | Where-Object { $_ -notin $entries })
    if ($unexpected.Count -or $missing.Count) {
        throw "runtime signing policy mismatch; unexpected unsigned=$($unexpected -join ','); missing named unsigned=$($missing -join ',')"
    }
    return [pscustomobject]@{ Signable = @($entries | Where-Object { $_ -match '\.(?:ps1|psm1)$' }); DeliberatelyUnsigned = $unsigned }
}
