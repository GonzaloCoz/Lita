@echo off
title Lita - Backend Go & Buscador Local
echo ===================================================
echo   Iniciando Lita Engine en http://localhost:8080
echo ===================================================
echo.
start "" "http://localhost:8080"
go run ./cmd/server
pause
