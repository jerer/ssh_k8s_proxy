//go:build embedded_agents

package embedded

import _ "embed"

//go:embed agents/linux-amd64.gz
var linuxAMD64 []byte

// LinuxAMD64 returns the gzip-compressed linux/amd64 agent embedded at build
// time.
func LinuxAMD64() ([]byte, bool) {
	if len(linuxAMD64) == 0 {
		return nil, false
	}
	return linuxAMD64, true
}
