// Package livetest holds the scenarios of agent-platform/control-plane as
// tests against the running compose project: run.sh starts it under
// rootless podman with test certificates and fake credentials, and runs
// them with CP_LIVE=1. Without it they skip.
package livetest
