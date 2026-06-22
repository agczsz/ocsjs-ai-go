@echo off
echo Building ocsjs-ai-answer-service in Go...
go build -ldflags "-s -w" -o ocs_ai_answer_service.exe
if %ERRORLEVEL% EQU 0 (
    echo Build successful: ocs_ai_answer_service.exe
) else (
    echo Build failed!
)
pause
