@echo off
rem 内阁：放行 9527 端口入站（真机通过 WiFi 连接电脑必需，否则会被 Windows 防火墙拦截）。
rem 用法：右键本文件 → 以管理员身份运行，看到"放行成功"即可。
netsh advfirewall firewall show rule name="cabinet-wss-9527" >nul 2>&1
if %errorlevel%==0 (
    echo 放行规则已存在，无需重复添加。
    pause
    exit /b 0
)
netsh advfirewall firewall add rule name="cabinet-wss-9527" dir=in action=allow protocol=TCP localport=9527
if %errorlevel%==0 (
    echo 放行成功：已允许局域网设备访问 9527 端口。
) else (
    echo 失败：请右键本文件，选择"以管理员身份运行"后重试。
)
pause
