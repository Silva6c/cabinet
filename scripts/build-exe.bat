@echo off
rem ============================================================
rem 一键构建托盘版 cabinet.exe（windowsgui 子系统）
rem ------------------------------------------------------------
rem 为什么必须带 -ldflags "-H windowsgui"（2026-10-01 教训）：
rem   裸 go build 产物是 console 子系统（PE Subsystem=CUI），
rem   双击 exe 会同时弹出一个 console 黑框。
rem   windowsgui 下进程没有 stdout——默认构建日志只写 logs/cabinet.log
rem   （internal/logging/console_off.go 已处理，勿开 MultiWriter 前
rem   置否则会吞掉文件写入）；控制台 UI 联调请用 -tags console 出
rem   cabinet-console.exe，二者互不干扰。
rem 用法：双击本文件，或在任意目录执行 scripts\build-exe.bat
rem ============================================================
setlocal
cd /d "%~dp0.."

rem CGO 必须开：Fyne 的 GL 后端（go-gl/glfw）需要 MinGW-w64 gcc
set "CGO_ENABLED=1"
set "PATH=%~dp0..\.tools\mingw64-extract\mingw64\bin;%PATH%"

where gcc >nul 2>nul
if errorlevel 1 (
    echo [错误] 找不到 gcc：确认 .tools\mingw64-extract\mingw64 存在
    pause
    exit /b 1
)

echo [1/2] 构建中：go build -ldflags "-H windowsgui [-X ...Version=vc<N>-<日期>]" -o cabinet.exe ./cmd/cabinet
rem 图标说明：cmd\cabinet\rsrc_windows_amd64.syso 由 resource.rc（1 ICON cabinet.ico）
rem 经 windres 生成，go build 自动链接——exe 图标与托盘图标同源（internal\tray\cabinet.ico）。
rem 换图标流程：改 internal\tray\cabinet.ico（或放 exe 目录 assets\icon\cabinet.ico 运行期覆盖）
rem → 复制到 cmd\cabinet\cabinet.ico → windres -i resource.rc -O coff -o rsrc_windows_amd64.syso
rem
rem 版本注入：protocol.go 的 Version（var，默认 0.1.0-dev）构建期写入
rem vc<git提交数>-<HEAD日期>；git 不可用时 VER_X 留空回退默认值，不影响构建。
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
    echo [错误] 构建失败
    pause
    exit /b 1
)

echo [2/2] 校验 PE 子系统（应显示 Windows GUI）:
.tools\mingw64-extract\mingw64\bin\objdump -p cabinet.exe | findstr /C:"Subsystem"
echo.
echo 完成。日志见 logs\cabinet.log；正在运行时请先退出托盘程序再覆盖。
pause
