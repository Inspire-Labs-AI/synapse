# run.ps1 — load backend/.env into the process environment and start the server.
#
# The Go config layer (internal/config) reads real environment variables and has
# no built-in .env loader, so this script bridges the documented .env file to the
# environment on Windows. Usage:  cd backend ;  .\run.ps1
#
# .env is authoritative: every run re-applies values from .env (even in the same
# terminal), so just edit .env to change config. To ingest a repo at startup, set
# SYNAPSE_INGEST_ROOT in .env.

$envFile = Join-Path $PSScriptRoot ".env"
if (Test-Path $envFile) {
  Get-Content $envFile | ForEach-Object {
    $line = $_.Trim()
    if ($line -and -not $line.StartsWith("#") -and $line.Contains("=")) {
      $idx = $line.IndexOf("=")
      $key = $line.Substring(0, $idx).Trim()
      $val = $line.Substring($idx + 1).Trim()
      # Always apply .env (authoritative). A process env var set by a previous run
      # in the same terminal would otherwise shadow an edited .env value.
      [System.Environment]::SetEnvironmentVariable($key, $val, "Process")
    }
  }
  Write-Host "[run.ps1] loaded env from .env" -ForegroundColor DarkGray
}

Write-Host "[run.ps1] embed=$env:SYNAPSE_EMBED_PROVIDER/$env:SYNAPSE_EMBED_MODEL  llm=$env:SYNAPSE_LLM_PROVIDER/$env:SYNAPSE_LLM_MODEL  ingest=$env:SYNAPSE_INGEST_ROOT" -ForegroundColor Cyan
go run ./cmd/server
