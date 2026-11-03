function Get-AgentBReadOnlyCommand {
    param([Parameter(Mandatory = $true)][string]$Text)

    $tokens = $null
    $parseErrors = $null
    $tree = [System.Management.Automation.Language.Parser]::ParseInput($Text, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count -gt 0) {
        return [pscustomobject]@{ Command = '<parse>'; Resolved = ''; Allowed = $false; Reason = $parseErrors[0].Message }
    }

    $redirection = $tree.FindAll({ param($node) $node -is [System.Management.Automation.Language.FileRedirectionAst] }, $true) | Select-Object -First 1
    if ($null -ne $redirection) {
        return [pscustomobject]@{ Command = $redirection.Extent.Text; Resolved = ''; Allowed = $false; Reason = 'file redirection is not read-only' }
    }
    $memberCall = $tree.FindAll({ param($node) $node -is [System.Management.Automation.Language.InvokeMemberExpressionAst] }, $true) | Select-Object -First 1
    if ($null -ne $memberCall) {
        return [pscustomobject]@{ Command = $memberCall.Extent.Text; Resolved = ''; Allowed = $false; Reason = '.NET method calls are not read-only' }
    }
    $environmentWrite = $tree.FindAll({
        param($node)
        if ($node -isnot [System.Management.Automation.Language.AssignmentStatementAst]) { return $false }
        $left = $node.Left
        return $left -is [System.Management.Automation.Language.VariableExpressionAst] -and $left.VariablePath.UserPath -like 'env:*'
    }, $true) | Select-Object -First 1
    if ($null -ne $environmentWrite) {
        return [pscustomobject]@{ Command = $environmentWrite.Left.Extent.Text; Resolved = ''; Allowed = $false; Reason = 'environment assignments are not read-only' }
    }

    $readVerbs = @('Get', 'Find', 'Test', 'Measure', 'Show', 'Select', 'Sort', 'Group', 'Format', 'Compare', 'Resolve', 'ConvertTo', 'ConvertFrom', 'Where', 'Search', 'Join', 'Split', 'Wait')
    $exceptions = @('Write-Output', 'Write-Host', 'Write-Verbose', 'Write-Warning', 'Write-Information', 'ForEach-Object', 'Out-String', 'Out-Null', 'Out-Default', 'Import-Module', 'Select-Object', 'Select-String')
    foreach ($commandAst in $tree.FindAll({ param($node) $node -is [System.Management.Automation.Language.CommandAst] }, $true)) {
        $written = $commandAst.GetCommandName()
        if ([string]::IsNullOrWhiteSpace($written)) {
            return [pscustomobject]@{ Command = $commandAst.Extent.Text; Resolved = ''; Allowed = $false; Reason = 'dynamic command names are not read-only' }
        }
        $resolved = $written
        $alias = Get-Command -Name $written -CommandType Alias -ErrorAction Ignore | Select-Object -First 1
        if ($null -ne $alias) {
            $resolved = $alias.ResolvedCommand.Name
            if ([string]::IsNullOrWhiteSpace($resolved)) { $resolved = $alias.Definition }
        }
        $definition = Get-Command -Name $resolved -ErrorAction Ignore | Select-Object -First 1
        if ($null -ne $definition -and ($definition.CommandType -eq 'Application' -or $definition.CommandType -eq 'ExternalScript')) {
            return [pscustomobject]@{ Command = $written; Resolved = $resolved; Allowed = $false; Reason = 'native executables and external scripts are not read-only' }
        }
        $verb = if ($resolved -match '^([^-]+)-') { $Matches[1] } else { '' }
        $allowed = $exceptions -contains $resolved -or $readVerbs -contains $verb
        if (-not $allowed) {
            $reason = if ($alias) { "alias resolves to $resolved, whose verb $verb is not read-only" } else { "verb $verb is not read-only" }
            return [pscustomobject]@{ Command = $written; Resolved = $resolved; Allowed = $false; Reason = $reason }
        }
        [pscustomobject]@{ Command = $written; Resolved = $resolved; Allowed = $true; Reason = '' }
    }
}

function Test-AgentBReadOnlyCommand {
    param([Parameter(Mandatory = $true)][string]$Text)
    $walk = @(Get-AgentBReadOnlyCommand -Text $Text)
    $refused = $walk | Where-Object { -not $_.Allowed } | Select-Object -First 1
    if ($null -eq $refused) { return '' }
    $name = if ([string]::IsNullOrWhiteSpace($refused.Command)) { '<command>' } else { $refused.Command }
    return "read-only profile: $($name): $($refused.Reason)"
}
