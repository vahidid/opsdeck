# Installs the latest opsdeck release on Windows (x64).
#
#   irm https://vahidid.github.io/opsdeck/install.ps1 | iex
#
# Downloads the installer, checks it against the release's SHA256SUMS.txt and
# runs it (Windows asks for permission to install into Program Files).
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'  # much faster downloads in Windows PowerShell
$repo = 'vahidid/opsdeck'
$headers = @{ 'User-Agent' = 'opsdeck-install'; 'Accept' = 'application/vnd.github+json' }

$rel = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest" -Headers $headers
$setup = $rel.assets | Where-Object { $_.name -like '*-windows-x64-setup.exe' } | Select-Object -First 1
$sums = $rel.assets | Where-Object { $_.name -eq 'SHA256SUMS.txt' } | Select-Object -First 1
if (-not $setup) { throw "the latest release ($($rel.tag_name)) has no Windows installer" }
if (-not $sums) { throw "the latest release has no SHA256SUMS.txt" }

$dir = Join-Path ([System.IO.Path]::GetTempPath()) ("opsdeck-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $dir | Out-Null
try {
  $file = Join-Path $dir $setup.name
  Write-Host "==> Downloading opsdeck $($rel.tag_name)"
  Invoke-WebRequest $setup.browser_download_url -OutFile $file -UseBasicParsing
  Invoke-WebRequest $sums.browser_download_url -OutFile (Join-Path $dir 'SHA256SUMS.txt') -UseBasicParsing

  $line = Get-Content (Join-Path $dir 'SHA256SUMS.txt') | Where-Object { ($_ -split '\s+')[-1].TrimStart('*') -eq $setup.name } | Select-Object -First 1
  if (-not $line) { throw "SHA256SUMS.txt has no entry for $($setup.name)" }
  $want = ($line -split '\s+')[0].ToLower()
  $got = (Get-FileHash $file -Algorithm SHA256).Hash.ToLower()
  if ($got -ne $want) { throw "checksum mismatch for $($setup.name)" }

  Write-Host "==> Installing"
  $p = Start-Process -FilePath $file -ArgumentList '/S' -Wait -PassThru
  if ($p.ExitCode -ne 0) { throw "the installer exited with code $($p.ExitCode)" }
  Write-Host "==> Installed opsdeck $($rel.tag_name) - start it from the Start menu"
} finally {
  Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
}
