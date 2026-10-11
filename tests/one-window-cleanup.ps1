if (-not ('AgentBInvariant.LockingProcesses' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;

namespace AgentBInvariant {
    public static class LockingProcesses {
        const int MoreData = 234;
        const int MaxAppName = 255;
        const int MaxServiceName = 63;

        [StructLayout(LayoutKind.Sequential)]
        struct UniqueProcess { public int ProcessId; public System.Runtime.InteropServices.ComTypes.FILETIME StartTime; }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        struct ProcessInfo {
            public UniqueProcess Process;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = MaxAppName + 1)] public string AppName;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = MaxServiceName + 1)] public string ServiceName;
            public uint AppType;
            public uint AppStatus;
            public uint TSSessionId;
            [MarshalAs(UnmanagedType.Bool)] public bool Restartable;
        }

        [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)] static extern int RmStartSession(out uint handle, int flags, StringBuilder key);
        [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)] static extern int RmRegisterResources(uint handle, uint fileCount, string[] files, uint appCount, IntPtr apps, uint serviceCount, string[] services);
        [DllImport("rstrtmgr.dll")] static extern int RmGetList(uint handle, out uint needed, ref uint count, [In, Out] ProcessInfo[] affected, ref uint reasons);
        [DllImport("rstrtmgr.dll")] static extern int RmEndSession(uint handle);

        public static string[] Find(string[] paths) {
            if (paths == null || paths.Length == 0) return Array.Empty<string>();
            uint handle;
            var key = new StringBuilder(64);
            int error = RmStartSession(out handle, 0, key);
            if (error != 0) return new[] { "unknown Restart Manager error " + error };
            try {
                error = RmRegisterResources(handle, (uint)paths.Length, paths, 0, IntPtr.Zero, 0, null);
                if (error != 0) return new[] { "unknown Restart Manager error " + error };
                uint needed = 0, count = 0, reasons = 0;
                error = RmGetList(handle, out needed, ref count, null, ref reasons);
                if (error == 0) return Array.Empty<string>();
                if (error != MoreData) return new[] { "unknown Restart Manager error " + error };
                var rows = new ProcessInfo[needed];
                count = needed;
                error = RmGetList(handle, out needed, ref count, rows, ref reasons);
                if (error != 0) return new[] { "unknown Restart Manager error " + error };
                var result = new List<string>();
                for (int index = 0; index < count; index++) {
                    int pid = rows[index].Process.ProcessId;
                    string name = rows[index].AppName;
                    try { name = Process.GetProcessById(pid).ProcessName + ".exe"; } catch {}
                    result.Add(name + " (PID " + pid + ")");
                }
                return result.ToArray();
            } finally { RmEndSession(handle); }
        }
    }
}
'@
}

function Get-OneWindowLockHolders {
    param([Parameter(Mandatory = $true)][string]$Path)
    $files = @()
    if (Test-Path -LiteralPath $Path -PathType Leaf) { $files = @([IO.Path]::GetFullPath($Path)) }
    elseif (Test-Path -LiteralPath $Path -PathType Container) {
        $files = @(Get-ChildItem -LiteralPath $Path -File -Recurse -Force -ErrorAction SilentlyContinue | ForEach-Object FullName)
    }
    if (-not $files.Count) { return @() }
    return @([AgentBInvariant.LockingProcesses]::Find($files) | Sort-Object -Unique)
}

function Wait-RemoveOneWindowRoot {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$AllowedRoot,
        [int]$MaxWaitSeconds = 120,
        [int]$RetrySeconds = 10
    )
    $deadline = [DateTime]::UtcNow.AddSeconds($MaxWaitSeconds)
    $lastError = $null
    do {
        try {
            Remove-TreeWithinAllowedRoots -Path $Path -AllowedRoots @($AllowedRoot) -Purpose 'one-window invariant cleanup'
            return
        } catch {
            $lastError = $_.Exception.Message
            if ([DateTime]::UtcNow -ge $deadline) { break }
            Start-Sleep -Seconds ([Math]::Min($RetrySeconds, [Math]::Max(1, [Math]::Ceiling(($deadline - [DateTime]::UtcNow).TotalSeconds))))
        }
    } while ([DateTime]::UtcNow -lt $deadline)
    $holders = @(Get-OneWindowLockHolders -Path $Path)
    $holderText = if ($holders.Count) { $holders -join ', ' } else { 'unknown' }
    throw "one-window invariant cleanup remained locked after $MaxWaitSeconds seconds; holder(s): $holderText; last error: $lastError"
}
