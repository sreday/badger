# Badger 2.0 installer for Windows (PowerShell):
#
#   irm https://raw.githubusercontent.com/sreday/badger/main/install.ps1 | iex
#
# Moves the old Badger (badger-windows-*.exe downloads) to the Recycle Bin,
# keeps its printer settings, installs badger2 and opens it at
# http://localhost:8090. Run it again to update.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repo = 'sreday/badger'
Write-Host 'Badger 2.0 installer'

# The processor, asked of Windows itself: a 32-bit PowerShell on 64-bit
# Windows, or an Intel-mode PowerShell on an ARM laptop, reports the wrong one.
$osArch = $null
try { $osArch = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch {}
if (-not $osArch) { $osArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE } }
switch -Regex ($osArch) {
    'Arm64' { $arch = 'arm64' }
    'X64|AMD64' { $arch = 'amd64' }
    default { Write-Host "Sorry, Badger 2.0 needs 64-bit Windows (this one is: $osArch)."; return }
}
Write-Host "This computer: Windows, $arch"
$asset = "badger2-windows-$arch.exe"

# The newest release with a Badger 2.0 build (pre-releases included).
$releases = Invoke-RestMethod "https://api.github.com/repos/$repo/releases?per_page=30"
$url = $null
foreach ($r in $releases) {
    $a = $r.assets | Where-Object { $_.name -eq $asset } | Select-Object -First 1
    if ($a) { $url = $a.browser_download_url; break }
}
if (-not $url) { Write-Host "Couldn't find a Badger 2.0 download for Windows $arch. Ask Mark or Miko."; return }

$home2 = Join-Path $HOME '.badger'
New-Item -ItemType Directory -Force $home2 | Out-Null

# --- Remove the old Badger -------------------------------------------------
Add-Type -AssemblyName Microsoft.VisualBasic
$dirs = @((Join-Path $HOME 'Downloads'), (Join-Path $HOME 'Desktop'), $HOME)
foreach ($dir in $dirs) {
    if (-not (Test-Path $dir)) { continue }
    foreach ($f in Get-ChildItem -Path $dir -Filter 'badger-windows-*.exe' -File -ErrorAction SilentlyContinue) {
        # Only Badger itself (other tools are also called badger).
        if (-not (Select-String -Path $f.FullName -Pattern 'sreday/badger' -SimpleMatch -Quiet)) { continue }
        $cfg = Join-Path $dir 'config.json'
        $keep = Join-Path $home2 'config.json'
        if ((Test-Path $cfg) -and -not (Test-Path $keep)) {
            Copy-Item $cfg $keep
            Write-Host "Kept your printer settings from $cfg"
        }
        Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $f.FullName } | Stop-Process -Force
        [Microsoft.VisualBasic.FileIO.FileSystem]::DeleteFile($f.FullName, 'OnlyErrorDialogs', 'SendToRecycleBin')
        Write-Host "Moved the old Badger to the Recycle Bin: $($f.FullName)"
    }
}

# --- Install Badger 2.0 ----------------------------------------------------
$bindir = Join-Path $env:LOCALAPPDATA 'Badger'
New-Item -ItemType Directory -Force $bindir | Out-Null
$exe = Join-Path $bindir 'badger2.exe'
Get-Process -Name 'badger2' -ErrorAction SilentlyContinue | Stop-Process -Force
Write-Host "Downloading $url"
$tmp = Join-Path $bindir 'badger2.new.exe' # Windows only runs .exe files
Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
# It must start here before it replaces anything.
$ver = $null
try { $ver = (& $tmp version | Out-String).Trim() } catch { $ver = $null }
if (-not $ver) {
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
    Write-Host "Badger 2.0 couldn't start on this computer. It needs 64-bit Windows 10 or later."
    return
}
Move-Item -Force $tmp $exe

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $bindir) {
    [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $bindir), 'User')
    $env:Path += ';' + $bindir
}

Write-Host "Installed: $exe ($ver)"
Write-Host ''
Write-Host 'Opening Badger 2.0 at http://localhost:8090'
Write-Host 'Keep this window open while you print. Next time, just type: badger2'
Write-Host ''
& $exe
