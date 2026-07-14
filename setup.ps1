# Project Synapse — install all dependencies.
# The Windows-friendly equivalent of `make setup`. Usage (from repo root):
#   .\setup.ps1
#
# Installs: the TypeScript parser subprocess deps (the ones whose absence makes
# .ts/.tsx ingestion fail), the Next.js frontend deps, and the Go modules.

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot

$steps = @(
    @{ msg = "TypeScript parser deps (backend/tools/tsparser)"; dir = "backend\tools\tsparser"; cmd = { npm install } },
    @{ msg = "Frontend deps (frontend)";                        dir = "frontend";               cmd = { npm install } },
    @{ msg = "Go modules (backend)";                            dir = "backend";                cmd = { go mod download } }
)

foreach ($s in $steps) {
    Write-Host "-> Installing $($s.msg) ..." -ForegroundColor Cyan
    Push-Location (Join-Path $root $s.dir)
    try {
        & $s.cmd
        if ($LASTEXITCODE -ne 0) { throw "command exited with code $LASTEXITCODE" }
    } finally {
        Pop-Location
    }
}

Write-Host ""
Write-Host "Setup complete." -ForegroundColor Green
Write-Host "  Next steps:" -ForegroundColor DarkGray
Write-Host "    1. Start the database:  cd docker; docker compose up -d" -ForegroundColor DarkGray
Write-Host "    2. Configure:           copy backend\.env.example backend\.env  (add keys, optional)" -ForegroundColor DarkGray
Write-Host "    3. Backend:             cd backend; .\run.ps1" -ForegroundColor DarkGray
Write-Host "    4. Frontend:            cd frontend; npm run dev" -ForegroundColor DarkGray
