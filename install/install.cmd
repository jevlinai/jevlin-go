@echo off
rem Jevlin installer for Windows CMD. Runs the PowerShell installer.
rem
rem   curl -fsSL https://jevlin.ai/install.cmd -o install.cmd && install.cmd && del install.cmd
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://jevlin.ai/install.ps1 | iex"
