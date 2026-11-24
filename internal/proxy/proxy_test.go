package proxy

import "testing"

func TestAgentDownloadURL(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "release",
			opts: Options{Version: "v1.2.3", AgentArch: "amd64"},
			want: "https://github.com/jerer/ssh_k8s_proxy/releases/download/v1.2.3/kubessh-linux-amd64",
		},
		{
			name: "dev uses latest",
			opts: Options{Version: "dev", AgentArch: "amd64"},
			want: "https://github.com/jerer/ssh_k8s_proxy/releases/latest/download/kubessh-linux-amd64",
		},
		{
			name: "empty version uses latest",
			opts: Options{AgentArch: "arm64"},
			want: "https://github.com/jerer/ssh_k8s_proxy/releases/latest/download/kubessh-linux-arm64",
		},
		{
			name: "template",
			opts: Options{
				Version:     "v9",
				AgentArch:   "arm64",
				DownloadURL: "https://example.test/{version}/{os}/{arch}/kubessh",
			},
			want: "https://example.test/v9/linux/arm64/kubessh",
		},
		{
			name: "literal template",
			opts: Options{DownloadURL: "file:///tmp/kubessh-agent"},
			want: "file:///tmp/kubessh-agent",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agentDownloadURL(test.opts); got != test.want {
				t.Fatalf("agentDownloadURL() = %q, want %q", got, test.want)
			}
		})
	}
}
