// Package edge contains socket and maintained DTLS adapters. These adapters own
// I/O, clocks, timers and workers; protocol machines are driven by the session
// owner. See DESIGN.md for ownership and lifecycle constraints.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md
package edge
