# Gate for narrated PPTX (V4.0 / G0-8): generate a deck, verify the package,
# then ask PowerPoint to open it, save it, and reopen the saved copy.
#
# Why PowerShell: the client half is COM-only. Judging lives in Go
# (scripts/e2e/audiogate/verify) so the same assertions also run on Linux CI
# without PowerPoint -- "no PowerPoint here" must not mean "gate skipped".
#
# Usage (from repo root):
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e/audio_pptx_gate.ps1
#   ... -SkipClient      package structure only (no PowerPoint needed)
#   ... -Pages 6         narrate fewer pages
#   ... -OutDir tmp/audio-gate
#
# Exit 0 = passed. Anything else = failed, with the reason printed.
#
# ASCII only on purpose: Windows PowerShell 5.1 decodes a BOM-less .ps1 as
# ANSI/GBK. A UTF-8 multi-byte sequence can then swallow the next ASCII byte --
# including a quote -- and the file fails to parse for reasons that look
# unrelated. Chinese rationale lives in scripts/fidelity/README.md.
param(
  [string]$OutDir = 'tmp/audio-gate',
  [int]$Pages = 14,
  [switch]$SkipClient,
  # Timeout for one COM open+save+reopen cycle. A file that needs repair blocks
  # Open() behind a modal dialog, so "took too long" is the repair signal --
  # the same criterion go-pptx L3 uses (TIMEOUT_REPAIR_PROMPT).
  [int]$ClientTimeoutSec = 90
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Push-Location $repo
try {
  New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
  $absOut = (Resolve-Path $OutDir).Path
  $pptx = Join-Path $absOut 'gate.pptx'
  $plan = Join-Path $absOut 'plan.json'
  $client = Join-Path $absOut 'client.json'
  $resaved = Join-Path $absOut 'gate.resaved.pptx'

  Write-Output '== 1/3 generate deck (same writer path as production) =='
  & go run ./scripts/e2e/audiogate -out $pptx -plan-out $plan -pages $Pages
  if ($LASTEXITCODE -ne 0) { Write-Output 'FAIL generation'; exit 1 }
  Write-Output ("    " + $pptx + "  " + (Get-Item $pptx).Length + " bytes")

  if ($SkipClient) {
    Write-Output '== 2/3 verify package structure =='
    & go run ./scripts/e2e/audiogate/verify -pptx $pptx -plan $plan
    Write-Output '== 3/3 client: skipped (-SkipClient) =='
  } else {
    Write-Output '== 2/3 client: PowerPoint open / save as / reopen =='
    # Remember existing POWERPNT instances so the timeout path only cleans up the
    # one this run created -- never a deck the user has open with unsaved work.
    $preexisting = @(Get-Process POWERPNT -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id)
    $job = Start-Job -ScriptBlock {
      param($script, $src, $dst, $out)
      & $script -Src $src -Dst $dst -Out $out
    } -ArgumentList (Join-Path $PSScriptRoot 'pptx_client.ps1'), $pptx, $resaved, $client
    if (Wait-Job $job -Timeout $ClientTimeoutSec) {
      $output = Receive-Job $job
      Remove-Job $job -Force
      Write-Output ("    " + ($output -join ' '))
      # PowerPoint 16 sometimes survives Quit() when the COM server was created
      # inside a background job. Reap only instances this run started.
      if ($preexisting.Count -eq 0) {
        Start-Sleep -Milliseconds 500
        Get-Process POWERPNT -ErrorAction SilentlyContinue |
          Where-Object { $preexisting -notcontains $_.Id } |
          Stop-Process -Force -ErrorAction SilentlyContinue
      }
      if ($output -match 'CLIENT=FAIL') {
        Write-Output 'FAIL client could not open or save the deck'
        exit 1
      }
    } else {
      Stop-Job $job
      Remove-Job $job -Force
      # A killed COM server keeps the file locked and makes the next run attach to
      # a broken instance; clean up only what this run started.
      Get-Process POWERPNT -ErrorAction SilentlyContinue |
        Where-Object { $preexisting -notcontains $_.Id } |
        Stop-Process -Force -ErrorAction SilentlyContinue
      Write-Output ("FAIL client did not return within ${ClientTimeoutSec}s: treated as REPAIR_PROMPT")
      Write-Output '     open the file by hand and look for a "needs repair" dialog'
      exit 1
    }
    Write-Output '== 3/3 verify structure + client read-back =='
    & go run ./scripts/e2e/audiogate/verify -pptx $pptx -plan $plan -client $client
  }
  if ($LASTEXITCODE -ne 0) { Write-Output 'FAIL assertions'; exit 1 }

  Write-Output ''
  Write-Output 'PASS. One thing COM cannot observe, please check by hand once:'
  Write-Output '  1. open gate.pptx and press F5: slide 1 must start playing by itself'
  Write-Output '  2. do not click; let it advance: each slide plays its own audio and the'
  Write-Output '     previous slide audio must stop (no cross-slide bleed)'
  Write-Output '  3. slide 3 has no audio: it should hold 1.5s then advance on its own'
  Write-Output '  4. save inside PowerPoint, reopen: audio icons and timings still there'
  Write-Output ("  file: " + $pptx)
  exit 0
} finally {
  Pop-Location
}
