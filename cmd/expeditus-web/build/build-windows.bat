@echo off
echo Building Expeditus Web for Windows...
cd /d "%~dp0\..\.."

GOOS=windows GOARCH=amd64 go build -o dist/expeditus-web-windows-amd64.exe ./cmd/expeditus-web/

echo Build complete: dist/expeditus-web-windows-amd64.exe
