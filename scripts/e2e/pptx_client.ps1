# Ask PowerPoint for facts about a generated deck; do not judge here.
#
# Judging lives in Go (scripts/e2e/audiogate/verify), so that "what the client
# says" and "what we expect" stay separate and the report can be pasted into an
# acceptance record as-is.
#
# Repair-prompt detection is the caller's job: a file that needs repair makes
# PowerPoint show a modal dialog, which blocks Open(). The orchestrator wraps
# this script in Start-Job + Wait-Job -Timeout and treats a timeout as
# REPAIR_PROMPT. Same criterion go-pptx L3 uses.
#
# NOTE: comments are ASCII only on purpose. Windows PowerShell 5.1 decodes a
# .ps1 without BOM as ANSI/GBK, so non-ASCII comments can break parsing once an
# editor drops the BOM. The reasoning in Chinese lives in scripts/fidelity/README.md.
param(
  [Parameter(Mandatory = $true)][string]$Src,
  [Parameter(Mandatory = $true)][string]$Dst,
  [Parameter(Mandatory = $true)][string]$Out
)

$ErrorActionPreference = 'Stop'
$msoMedia = 16

function Read-Presentation($pres) {
  $slides = @()
  for ($i = 1; $i -le $pres.Slides.Count; $i++) {
    $s = $pres.Slides.Item($i)
    $media = @()
    for ($j = 1; $j -le $s.Shapes.Count; $j++) {
      $sh = $s.Shapes.Item($j)
      if ($sh.Type -eq $msoMedia) {
        # MediaType 2 = ppMediaTypeSound (1 other, 2 sound, 3 movie): the reliable
        # "this is audio" signal. MediaFormat.AudioFormat returns null for embedded
        # WAV, and AnimationSettings.PlaySettings.PlayInBackground returns empty
        # here -- in PowerShell $null -ne 0 is True, so using it would report
        # "plays in background" for every file. Cross-slide playback is judged
        # from the package XML (numSld / onStopAudio) instead.
        $media += [pscustomobject]@{
          name      = $sh.Name
          mediaType = [int]$sh.MediaType
        }
      }
    }
    $effects = @()
    try {
      $seq = $s.TimeLine.MainSequence
      for ($k = 1; $k -le $seq.Count; $k++) {
        $eff = $seq.Item($k)
        $effects += [pscustomobject]@{
          effectType = $eff.EffectType
          duration   = [double]$eff.Timing.Duration
        }
      }
    } catch {
      # Accessing MainSequence on a slide without animation throws; empty is correct.
    }
    $slides += [pscustomobject]@{
      index          = $i - 1
      mediaShapes    = $media
      advanceOnTime  = ($s.SlideShowTransition.AdvanceOnTime -ne 0)
      advanceSeconds = [double]$s.SlideShowTransition.AdvanceTime
      effects        = $effects
    }
  }
  return $slides
}

$result = [pscustomobject]@{
  ok      = $false
  version = ''
  src     = $Src
  dst     = $Dst
  openOk  = $false
  saveOk  = $false
  slides  = @()
  resaved = @()
  error   = ''
}

$app = $null
# PowerPoint is single-instance: New-Object attaches to a running instance if
# there is one. Only the owner may Quit() -- otherwise this script would close
# a deck the user has open with unsaved work.
$ownsApp = (@(Get-Process POWERPNT -ErrorAction SilentlyContinue).Count -eq 0)
try {
  $app = New-Object -ComObject PowerPoint.Application
  $result.version = $app.Version
  # ppAlertsNone: keep "missing font" / "update links" prompts from blocking.
  $app.DisplayAlerts = 1

  # WithWindow=false: no window, no focus stealing.
  $pres = $app.Presentations.Open($Src, $false, $false, $false)
  $result.openOk = $true
  $result.slides = Read-Presentation $pres

  # 24 = ppSaveAsOpenXMLPresentation (.pptx)
  $pres.SaveAs($Dst, 24)
  $result.saveOk = $true
  $pres.Close()

  # Reopen what the client itself wrote: this is the "reopen and save" part.
  $again = $app.Presentations.Open($Dst, $false, $false, $false)
  $result.resaved = Read-Presentation $again
  $again.Close()

  $result.ok = $true
} catch {
  $result.error = $_.Exception.Message
} finally {
  if ($app -ne $null) {
    if ($ownsApp) {
      try { $app.Quit() } catch {}
    }
    try { [System.Runtime.Interopservices.Marshal]::ReleaseComObject($app) | Out-Null } catch {}
  }
}

# No BOM: Go parses this file and json.Unmarshal rejects a leading U+FEFF.
$json = $result | ConvertTo-Json -Depth 6
[System.IO.File]::WriteAllText($Out, $json, (New-Object System.Text.UTF8Encoding($false)))
if (-not $result.ok) {
  Write-Output ("CLIENT=FAIL " + $result.error)
  exit 1
}
Write-Output ("CLIENT=OK version=" + $result.version + " slides=" + $result.slides.Count + " save=ok")
