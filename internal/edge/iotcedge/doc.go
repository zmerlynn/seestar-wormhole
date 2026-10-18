// Package iotcedge owns attempt-local IOTC sockets, DNS and crypto effects.
// It never calls a pure machine: bounded action queues and completion callbacks
// connect its workers to the session owner. All workers join on cancellation.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/index.md
package iotcedge
