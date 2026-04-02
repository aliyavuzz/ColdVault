@echo off
echo ============================================
echo   ColdVault Build System
echo ============================================
echo.

cd /d "%~dp0"

echo [1/4] Tidying modules...
go mod tidy
if %ERRORLEVEL% NEQ 0 (
    echo ERROR: go mod tidy failed
    pause
    exit /b 1
)

echo [2/4] Building Hunter (The Silent Hunter)...
go build -ldflags="-s -w" -o bin\coldvault-hunter.exe .\cmd\hunter
if %ERRORLEVEL% NEQ 0 (
    echo ERROR: hunter build failed
    pause
    exit /b 1
)

echo [3/4] Building Daemon (The Heavy Lifter)...
go build -ldflags="-s -w" -o bin\coldvault-daemon.exe .\cmd\daemon
if %ERRORLEVEL% NEQ 0 (
    echo ERROR: daemon build failed
    pause
    exit /b 1
)

echo [4/4] Building UI (Native Win32)...
:: Note: lxn/walk requires a manifest. If rsrc.exe is available, embed it.
:: Otherwise build without -H windowsgui for debugging.
go build -ldflags="-s -w -H windowsgui" -o bin\coldvault-ui.exe .\cmd\ui 2>nul
if %ERRORLEVEL% NEQ 0 (
    echo WARNING: GUI build failed (may need rsrc.exe for manifest embedding)
    echo Building without GUI subsystem flag...
    go build -ldflags="-s -w" -o bin\coldvault-ui.exe .\cmd\ui
)

echo.
echo Building stub restorer...
go build -ldflags="-s -w" -o bin\restore.exe .\cmd\stub

echo.
:: Copy configs
if not exist bin\configs mkdir bin\configs
copy /Y configs\*.json bin\configs\ >nul

echo ============================================
echo   Build Complete!
echo ============================================
echo.
echo Binaries in .\bin\:
dir /b bin\*.exe
echo.
echo Config files in .\bin\configs\
echo.
echo Next steps:
echo   1. Edit bin\configs\coldvault.json with your paths
echo   2. Run: bin\coldvault-daemon.exe run --dry-run
echo   3. Run: bin\coldvault-daemon.exe install
echo   4. Or launch: bin\coldvault-ui.exe
echo.
pause
