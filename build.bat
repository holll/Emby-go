@echo off
setlocal enabledelayedexpansion

REM ======================================================
REM         Emby-Go Build Script
REM   Targets: windows/amd64, linux/amd64
REM   Entry:   ./cmd/metatube
REM   Version: git tag + commit + build time
REM ======================================================

REM ---- version info ----
for /f "delims=" %%a in ('git describe --tags --abbrev^=0 2^>nul') do set VERSION=%%a
if "!VERSION!"=="" set VERSION=0.0.0

for /f "delims=" %%a in ('git rev-parse --short HEAD 2^>nul') do set COMMIT=%%a
if "!COMMIT!"=="" set COMMIT=unknown

for /f "delims=" %%a in ('powershell -NoProfile -command "[System.TimeZoneInfo]::ConvertTimeBySystemTimeZoneId((Get-Date),'China Standard Time').ToString('yyyy-MM-dd_HH-mm-ss')"') do set BUILDTIME=%%a
if "!BUILDTIME!"=="" set BUILDTIME=unknown

set OUTDIR=dist
if not exist %OUTDIR% mkdir %OUTDIR%

> %OUTDIR%\version.txt echo Version=%VERSION%
>> %OUTDIR%\version.txt echo Commit=%COMMIT%
>> %OUTDIR%\version.txt echo BuildTime=%BUILDTIME%

echo Version: %VERSION%
echo Commit:  %COMMIT%
echo Time:    %BUILDTIME%
echo.

set LDFLAGS=-s -w -X main.version=%VERSION% -X main.commit=%COMMIT% -X main.buildTime=%BUILDTIME%

REM ---- build loop ----
for %%T in (windows/amd64 linux/amd64) do (
    for /f "tokens=1,2 delims=/" %%a in ("%%T") do (
        set GOOS=%%a
        set GOARCH=%%b
        set CGO_ENABLED=0
        set EXT=
        if "!GOOS!"=="windows" set EXT=.exe
        set OUTFILE=%OUTDIR%\emby-go_!GOOS!_!GOARCH!!EXT!

        echo ---------------------------------------------------
        echo Building !OUTFILE!
        echo ---------------------------------------------------

        go build -o "!OUTFILE!" -ldflags "!LDFLAGS!" ./cmd/metatube
        if errorlevel 1 (
            echo.
            echo Build failed: !GOOS!/!GOARCH!
            pause
            exit /b 1
        )

        where upx >nul 2>nul
        if !errorlevel!==0 (
            echo Compressing with UPX...
            upx --best --lzma "!OUTFILE!"
        )
    )
)

echo.
echo ===========================================
echo All builds completed successfully!
echo Binaries in: %OUTDIR%
echo ===========================================
echo.

pause
