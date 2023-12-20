#!/bin/bash

sigterm_handler() {
    echo "Received SIGTERM signal. Delaying termination for 60 seconds..."
    sleep 60
    kill -TERM "$INTERNAL_PROCESS_PID"
    wait "$INTERNAL_PROCESS_PID"
}
trap 'sigterm_handler' SIGTERM
ossfs "$@" &
INTERNAL_PROCESS_PID="$!"
wait "$INTERNAL_PROCESS_PID"

