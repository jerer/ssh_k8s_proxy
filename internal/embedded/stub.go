//go:build !embedded_agents

// Package embedded optionally provides a prebuilt Linux agent inside a macOS
// binary. Build with -tags embedded_agents after generating
// agents/linux-amd64.gz to enable it.
package embedded

// LinuxAMD64 returns the compressed linux/amd64 agent when it was embedded at
// build time.
func LinuxAMD64() ([]byte, bool) { return nil, false }
