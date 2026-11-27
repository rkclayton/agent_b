param([Parameter(Mandatory = $true)][string]$InputPath)

$ErrorActionPreference = 'Stop'
$culture = [Globalization.CultureInfo]::InvariantCulture
$file = Get-Item -LiteralPath $InputPath
if ($file.Length -gt 512KB) { throw 'Watch input exceeds 512 KiB.' }
$request = Get-Content -LiteralPath $InputPath -Raw -Encoding UTF8 | ConvertFrom-Json
$watch = $request.watch
$slug = [string]$request.slug
if ($slug -cnotmatch '^[a-z0-9][a-z0-9-]{0,63}$' -or $watch.mode -cnotin @('landed', 'appearance')) {
    throw 'Supply a watch slug and either landed or appearance mode.'
}
if ([string]::IsNullOrWhiteSpace($watch.identity) -or [string]::IsNullOrWhiteSpace($watch.name)) {
    throw 'The watch needs an exact product identity and a name.'
}
$approved = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
foreach ($source in $watch.sellers) {
    $address = $null
    if (-not [Uri]::TryCreate($source, [UriKind]::Absolute, [ref]$address) -or
        $address.Scheme -notin @('https', 'http') -or $address.UserInfo) {
        throw 'Every confirmed seller must have a public product or listing address.'
    }
    [void]$approved.Add($source)
}
if ($approved.Count -lt 1 -or $approved.Count -gt 20) { throw 'Confirm one to twenty seller addresses.' }

function Number($value) {
    $parsed = 0D
    if ($null -ne $value -and [decimal]::TryParse([Convert]::ToString($value, $culture),
        [Globalization.NumberStyles]::AllowDecimalPoint, $culture, [ref]$parsed)) {
        return $parsed
    }
    return $null
}

$target = Number $watch.target
$tax = Number $watch.tax_pct
if ($watch.mode -eq 'landed' -and ($null -eq $target -or $target -le 0 -or $watch.currency -cnotmatch '^[A-Z]{3}$')) {
    throw 'A landed watch needs a positive target and a three-letter currency.'
}
if ($null -ne $watch.tax_pct -and ($null -eq $tax -or $tax -gt 100)) {
    throw 'Tax must be a percentage from 0 to 100.'
}

if ($request.action -ceq 'start') {
    @{
        watch = $watch
        job = @{
            action = 'create'
            name = "price-watch $slug"
            schedule = 'every 6h'
            skills = @('price-watch')
            prompt = "Check price watch $slug."
        }
    } | ConvertTo-Json -Depth 8 -Compress
    exit 0
}
if ($request.action -cne 'check') { throw 'Use start or check.' }

$best = $null
$notice = '[SILENT]'
foreach ($offer in $request.offers) {
    if (-not $approved.Contains([string]$offer.source) -or $offer.identity -cne $watch.identity -or
        $offer.bundle -isnot [bool] -or $offer.bundle -or $offer.available -isnot [bool] -or -not $offer.available) {
        continue
    }
    if ($watch.mode -eq 'appearance') {
        if (-not $watch.appeared) {
            $notice = "$($watch.name) is now listed.`n$($offer.source)"
            $watch | Add-Member -NotePropertyName appeared -NotePropertyValue $true -Force
        }
        break
    }
    $price = Number $offer.price
    $shipping = Number $offer.shipping
    if ($offer.currency -cne $watch.currency -or $null -eq $price -or $price -le 0 -or $null -eq $shipping) {
        continue
    }
    $landed = [Math]::Round(($price + $shipping) * (1D + ([decimal]$tax / 100D)), 2, [MidpointRounding]::AwayFromZero)
    if ($null -eq $best -or $landed -lt $best.landed) {
        $best = @{ source = $offer.source; price = $price; shipping = $shipping; landed = $landed }
    }
}
if ($null -ne $best) {
    $watch | Add-Member -NotePropertyName last -NotePropertyValue $best -Force
    $previous = Number $watch.alerted
    if (($null -eq $previous -and $best.landed -le $target) -or ($null -ne $previous -and $best.landed -lt $previous)) {
        $taxNote = if ($null -eq $tax) { 'tax not included' } else { 'tax included' }
        $amount = $best.landed.ToString('0.00', $culture)
        $notice = "$($watch.name) $amount $($watch.currency) landed ($taxNote).`n$($best.source)"
        $watch | Add-Member -NotePropertyName alerted -NotePropertyValue $best.landed -Force
    }
}
@{ watch = $watch; best = $best; notice = $notice } | ConvertTo-Json -Depth 8 -Compress
