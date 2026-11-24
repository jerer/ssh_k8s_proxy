//go:build windows

package agent

import "golang.org/x/crypto/ssh"

// handleSession rejects sessions on Windows. The agent is expected to run
// inside a Linux container; Windows is only supported as a client/proxy.
func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	for request := range requests {
		if request.WantReply {
			_ = request.Reply(false, nil)
		}
	}
	_ = channel.Close()
}
