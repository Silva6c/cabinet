@echo off
rem ============================================================
rem One-shot build for tray cabinet.exe (windowsgui subsystem).
rem
rem ASCII-only comments by design: this file is committed UTF-8
rem with no BOM, and cmd.exe on zh-CN systems parses batch files
rem in the GBK codepage -- multi-byte comment text there breaks
rem the parser mid-block (2026-10-29 live incident; the sibling
rem package-portable.bat survives only because it is GBK-encoded).
rem Keep every byte of this file below 0x80.
rem
rem Why -ldflags "-H windowsgui" (2026-10-01 lesson):
rem   a plain go build yields the console subsystem (PE CUI) and
rem   double-clicking the exe pops a stray console window.
rem   windowsgui processes have no stdout -- runtime logs go to
rem   logs/cabinet.log only (see internal/logging/console_off.go;
rem   do NOT enable the MultiWriter there or file writes break).
rem   For console-UI debugging build with -tags console into
rem   cabinet-console.exe; the two never interfere.
rem Usage: double-click, or run scripts\build-exe.bat anywhere.
rem ============================================================
setlocal
cd /d "%~dp0.."

rem CGO is mandatory: Fyne's GL backend (go-gl/glfw) needs MinGW-w64 gcc
set "CGO_ENABLED=1"
set "PATH=%~dp0..\.tools\mingw64-extract\mingw64\bin;%PATH%"

where gcc >nul 2>nul
if errorlevel 1 (
    echo [ERROR] gcc not found: confirm .tools\mingw64-extract\mingw64 exists
    pause
    exit /b 1
)

echo [1/2] Building: go build -ldflags "-H windowsgui [-X ...Version=vc<N>-<date>]" -o cabinet.exe ./cmd/cabinet
rem Icon: cmd\cabinet\rsrc_windows_amd64.syso generated via windres from
rem resource.rc (1 ICON cabinet.ico); go build links it automatically --
rem exe icon and tray icon share one source (internal\tray\cabinet.ico).
rem Icon swap flow: edit internal\tray\cabinet.ico (or drop assets\icon\cabinet.ico
rem next to the exe for runtime override) -> copy to cmd\cabinet\cabinet.ico
rem -> windres -i resource.rc -O coff -o rsrc_windows_amd64.syso
rem
rem Version injection: server.Version (var, default 0.1.0-dev) written at
rem build time as vc<git commit count>-<HEAD date>; VER_X stays empty when
rem git is unavailable and the build still succeeds with the default value.
set "GIT=git"
where git >nul 2>nul
if errorlevel 1 (
    if exist "%ProgramFiles%\Git\cmd\git.exe" (
        set "GIT=%ProgramFiles%\Git\cmd\git.exe"
    ) else if exist "%ProgramFiles(x86)%\Git\cmd\git.exe" (
        set "GIT=%ProgramFiles(x86)%\Git\cmd\git.exe"
    ) else (
        set "GIT="
    )
)
set "VC="
set "BD="
if defined GIT (
    for /f %%i in ('"%GIT%" rev-list HEAD --count 2^>nul') do set "VC=%%i"
    for /f %%i in ('"%GIT%" log -1 --format^=%%cd --date^=format:%%Y%%m%%d 2^>nul') do set "BD=%%i"
)
set "VER_X="
if defined VC if defined BD set "VER_X=-X cabinet/internal/server.Version=vc%VC%-%BD%"
go build -ldflags "-H windowsgui %VER_X%" -o cabinet.exe ./cmd/cabinet
if errorlevel 1 (
    echo [ERROR] build failed
    pause
    exit /b 1
)

echo [2/2] Verify PE subsystem (expect Windows GUI):
.tools\mingw64-extract\mingw64\bin\objdump -p cabinet.exe | findstr /C:"Subsystem"
echo.
echo Done. Logs: logs\cabinet.log; close the tray app before overwriting a running exe.
pause
