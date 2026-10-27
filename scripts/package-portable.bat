@echo off
rem Thin launcher for portable packaging (English-only on purpose: UTF-8 Chinese
rem in .bat gets mangled by cmd's codepage 936; all real logic lives in Go).
rem Usage: scripts\package-portable.bat [-lite] [-with-engine] [-with-books]
rem See tools/portable-pack/main.go for details (Chinese comments in Go code).
setlocal
cd /d "%~dp0.."

where go >nul 2>nul
if errorlevel 1 (
    echo [error] go not found in PATH
    pause
    exit /b 1
)

rem 本机 go run 有 fork/exec 问题（README 构建节约定），统一 go build 产物运行
if not exist dist mkdir dist
go build -o dist\portable-pack.exe .\tools\portable-pack
if errorlevel 1 (
    echo [error] build portable-pack failed
    pause
    exit /b 1
)

dist\portable-pack.exe %*
set "EC=%ERRORLEVEL%"
pause
exit /b %EC%
