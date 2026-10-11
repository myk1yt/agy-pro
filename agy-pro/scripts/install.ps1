# agy-pro one-click installer for Windows
# Automatically installs Go toolchain if missing, builds agy.exe, sets up PATH,
# and initializes the agy-pro environment with zero manual configuration.

[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

function Write-Header {
    param([string]$text)
    Write-Host ""
    Write-Host "============================================================" -ForegroundColor Cyan
    Write-Host "  $text" -ForegroundColor Cyan
    Write-Host "============================================================" -ForegroundColor Cyan
}

function Write-Step {
    param([string]$text)
    Write-Host ">> $text" -ForegroundColor Yellow
}

function Write-Success {
    param([string]$text)
    Write-Host "[OK] $text" -ForegroundColor Green
}

function Write-Notice {
    param([string]$text)
    Write-Host "[INFO] $text" -ForegroundColor Gray
}

Write-Header "agy-pro 초간편 원클릭 자동 설치 마법사"
Write-Notice "Antigravity Go Shim 및 멀티 모델 스웜 환경을 자동으로 구성합니다."

# 1. Resolve paths
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$AgyProDir = Split-Path -Parent $ScriptDir
$UserHome = [Environment]::GetFolderPath("UserProfile")
$TargetHome = Join-Path $UserHome ".agy-pro"
$TargetBin = Join-Path $TargetHome "bin"
$TargetBackups = Join-Path $TargetHome "backups"
$TargetExe = Join-Path $TargetBin "agy.exe"

Write-Step "1/5 필수 디렉토리 확인 및 생성 중..."
$dirsToCreate = @($TargetHome, $TargetBin, $TargetBackups)
foreach ($dir in $dirsToCreate) {
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
}
Write-Success "기본 디렉토리 준비 완료 ($TargetHome)"

# 2. Check / Install Go toolchain
Write-Step "2/5 Go 언어 툴체인 점검 중..."
$GoExe = $null

# Check current PATH
$goInPath = Get-Command "go.exe" -ErrorAction SilentlyContinue
if ($goInPath) {
    $GoExe = $goInPath.Source
}

# Check Program Files
if (-not $GoExe -and (Test-Path "C:\Program Files\Go\bin\go.exe")) {
    $GoExe = "C:\Program Files\Go\bin\go.exe"
}

# Check ~/.agy-pro/go
$portableGo = Join-Path $TargetHome "go\bin\go.exe"
if (-not $GoExe -and (Test-Path $portableGo)) {
    $GoExe = $portableGo
}

if (-not $GoExe) {
    Write-Notice "Go가 시스템에 설치되어 있지 않습니다. 자동으로 설치를 시작합니다..."
    
    # Try winget first
    $wingetAvailable = Get-Command "winget.exe" -ErrorAction SilentlyContinue
    if ($wingetAvailable) {
        Write-Notice "winget을 통해 Go 공식 패키지 자동 설치를 시도합니다..."
        try {
            $wingetProc = Start-Process -FilePath "winget.exe" `
                -ArgumentList "install --id GoLang.Go -e --silent --accept-source-agreements --accept-package-agreements" `
                -NoNewWindow -Wait -PassThru
            if ($wingetProc.ExitCode -eq 0 -and (Test-Path "C:\Program Files\Go\bin\go.exe")) {
                $GoExe = "C:\Program Files\Go\bin\go.exe"
            }
        } catch {
            Write-Notice "winget 설치 시도 중 예외 발생: $_"
        }
    }

    # If winget did not complete, use portable Go zip fallback
    if (-not $GoExe) {
        Write-Notice "Go 무설치 포터블 패키지(공식 배포본)를 다운로드하여 직접 구성합니다..."
        $tempZip = Join-Path $env:TEMP "go1.23.6.windows-amd64.zip"
        $downloadUrl = "https://dl.google.com/go/go1.23.6.windows-amd64.zip"
        
        Write-Notice "다운로드 진행 중 ($downloadUrl)..."
        if (Get-Command "curl.exe" -ErrorAction SilentlyContinue) {
            & curl.exe -L $downloadUrl -o $tempZip --silent --show-error
        } else {
            Invoke-WebRequest -Uri $downloadUrl -OutFile $tempZip -UseBasicParsing
        }

        Write-Notice "압축 해제 중..."
        if (Get-Command "tar.exe" -ErrorAction SilentlyContinue) {
            & tar.exe -xf $tempZip -C $TargetHome
        } else {
            Expand-Archive -Path $tempZip -DestinationPath $TargetHome -Force
        }

        Remove-Item $tempZip -Force -ErrorAction SilentlyContinue

        if (Test-Path $portableGo) {
            $GoExe = $portableGo
        }
    }
}

if (-not $GoExe -or -not (Test-Path $GoExe)) {
    throw "Go 툴체인 구성에 실패했습니다. 인터넷 연결을 확인하거나 GoLang(https://go.dev)을 수동 설치해주세요."
}

# Add Go directory to process PATH
$goDir = Split-Path -Parent $GoExe
$env:PATH = "$goDir;$env:PATH"
$goVer = & $GoExe version
Write-Success "Go 툴체인 감지: $goVer"

# 3. Build agy-pro shim binary
Write-Step "3/5 agy-pro 심(Shim) 바이너리 빌드 중..."
Push-Location $AgyProDir
try {
    $tempBuildOutput = Join-Path $TargetBin "agy.tmp.exe"
    & $GoExe build -ldflags="-s -w" -o $tempBuildOutput ./cmd/agy
    if ($LASTEXITCODE -ne 0) {
        throw "Go 빌드 실패 (코드: $LASTEXITCODE)"
    }

    # Safely swap into target
    if (Test-Path $TargetExe) {
        Remove-Item $TargetExe -Force -ErrorAction SilentlyContinue
    }
    Move-Item -Path $tempBuildOutput -Destination $TargetExe -Force
} finally {
    Pop-Location
}
Write-Success "빌드 완료: $TargetExe"

# 4. Register ~/.agy-pro/bin to Windows User PATH (prepend to highest priority)
Write-Step "4/5 Windows 환경변수 PATH 최상단 등록 중..."
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $userPath) {
    $userPath = ""
}

$entries = $userPath.Split(';') | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" }
$cleanedEntries = @()
foreach ($entry in $entries) {
    if ($entry.TrimEnd('\') -ne $TargetBin.TrimEnd('\')) {
        $cleanedEntries += $entry
    }
}

# Prepend TargetBin to the top
$newUserPath = "$TargetBin;" + ($cleanedEntries -join ';')
[Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
$env:PATH = "$TargetBin;$env:PATH"
Write-Success "User PATH 최상단 등록 완료 ($TargetBin)"

# 5. Verification
Write-Step "5/5 설치 무결성 및 시스템 점검 실행 중..."
& $TargetExe pro doctor

Write-Header "설치가 성공적으로 완료되었습니다!"
Write-Host ""
Write-Host "축하합니다! agy-pro가 완벽하게 준비되었습니다." -ForegroundColor Green
Write-Host ""
Write-Host "  * 설치 위치: $TargetExe" -ForegroundColor White
Write-Host "  * 데이터 홈: $TargetHome" -ForegroundColor White
Write-Host ""
Write-Host "새 터미널(PowerShell 또는 CMD)을 열고 다음 명령어들을 실행해보세요:" -ForegroundColor Cyan
Write-Host "  1. agy pro setup  : 대화형 외부 AI 모델 (OpenRouter, DeepSeek 등) 연동 마법사" -ForegroundColor Yellow
Write-Host "  2. agy pro mode   : 풀스택(오케스트레이터) / 기본(클린) 모드 전환" -ForegroundColor Yellow
Write-Host "  3. agy pro doctor : 시스템 환경 및 도구 상태 진단" -ForegroundColor Yellow
Write-Host "  4. agy            : 기존 Antigravity 명령어 100% 투명 실행" -ForegroundColor Yellow
Write-Host ""
