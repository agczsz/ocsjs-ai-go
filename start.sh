#!/bin/bash
if [ ! -f ./ocs_ai_answer_service ]; then
    echo "Binary not found, building first..."
    ./build.sh
fi
echo "Starting ocs_ai_answer_service..."
./ocs_ai_answer_service
