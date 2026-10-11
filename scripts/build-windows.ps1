# Windows 11 x64: Python 3.13, Go 1.27.1, NSIS 3.11 are build-time tools only.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
$root = (Get-Location).Path
function Check { if ($LASTEXITCODE -ne 0) { throw "Build failed with exit code $LASTEXITCODE" } }
$env:GOCACHE = Join-Path $root '.build\go-cache'
New-Item -ItemType Directory -Force 'bin','.build','.build/windows-wheels' | Out-Null
Push-Location backend
go test ./...; Check
Pop-Location
py -3.13 -m venv .build/venv; Check
$python = Join-Path $root '.build/venv/Scripts/python.exe'
& $python -m pip install -r requirements-build.txt; Check
& $python scripts/render-icons.py; Check
& $python -m pip download --only-binary=:all: --platform win_amd64 --python-version 313 --implementation cp --abi cp313 --dest .build/windows-wheels 'PySide6==6.11.2'; Check
& $python scripts/assemble-windows.py; Check
$compiler = if ($env:NSIS_COMPILER) { $env:NSIS_COMPILER } else { 'C:\Program Files (x86)\NSIS\makensis.exe' }
& $compiler packaging/handoff.nsi; Check
Get-ChildItem dist/*windows*.exe,dist/*windows*.zip | Get-FileHash -Algorithm SHA256 | Format-Table -AutoSize | Out-File dist/SHA256SUMS-windows.txt
