@echo off
rem Jevlin installer for Windows CMD. Runs the PowerShell installer.
rem
rem   curl -fsSL https://jevlin.ai/install.cmd -o install.cmd && install.cmd && del install.cmd
rem
rem Windows PowerShell starts here without a PSModulePath, so it builds its own.
rem Run from a PowerShell 7 prompt, this script would otherwise hand it 7's module
rem path, and Windows PowerShell would load 7's modules and not find Get-FileHash.
setlocal
set "PSModulePath="
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://jevlin.ai/install.ps1 | iex"
