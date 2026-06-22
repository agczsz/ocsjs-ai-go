#!/bin/bash
echo "Building ocsjs-ai-answer-service in Go..."
go build -ldflags "-s -w" -o ocs_ai_answer_service
if [ $? -eq 0 ]; then
    echo "Build successful: ocs_ai_answer_service"
else
    echo "Build failed!"
    exit 1
fi
