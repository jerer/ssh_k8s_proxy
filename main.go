package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/jerer/ssh_k8s_proxy/internal/agent"
	"github.com/jerer/ssh_k8s_proxy/internal/digest"
	"github.com/jerer/ssh_k8s_proxy/internal/kube"
	"github.com/jerer/ssh_k8s_proxy/internal/proxy"
)

var version = "dev"

const defaultRemotePath = "/tmp/kubessh/kubessh"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "kubessh: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return errors.New("missing command")
	}

	switch args[0] {
	case "agent":
		return runAgent(args[1:])
	case "proxy":
		return runProxy(args[1:])
	case "inject":
		return runInject(args[1:])
	case "version", "--version", "-v":
		fmt.Println(version)
		return nil
	case "hash":
		sum, err := digest.Self()
		if err != nil {
			return err
		}
		fmt.Println(sum)
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	stdio := fs.Bool("stdio", false, "serve a single SSH connection over stdin/stdout (used by the proxy)")
	listen := fs.String("listen", envDefault("KUBESSH_LISTEN", "127.0.0.1:2222"), "TCP address to listen on when not using --stdio")
	hostKey := fs.String("host-key", envDefault("KUBESSH_HOST_KEY", agent.DefaultHostKeyPath), "path to the SSH host private key")
	shell := fs.String("shell", os.Getenv("KUBESSH_SHELL"), "login shell for sessions (default: $SHELL or /bin/sh)")
	workDir := fs.String("workdir", os.Getenv("KUBESSH_WORKDIR"), "working directory for sessions (default: $HOME)")
	password := fs.String("password", "", "SSH password (prefer KUBESSH_PASSWORD)")
	serverVersion := fs.String("server-version", "", "SSH server version banner")
	allowNoAuth := fs.Bool("allow-no-auth", false, "allow unauthenticated clients when listening on a network address")
	var authorizedKeys multiString
	var keyFiles multiString
	fs.Var(&authorizedKeys, "authorized-key", "authorized public key (repeatable)")
	fs.Var(&keyFiles, "authorized-keys", "authorized_keys file (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if !agentSupported() {
		return errors.New("agent mode is only supported on Linux and macOS")
	}

	logger := newLogger()
	opts := agent.Options{
		HostKeyPath:   *hostKey,
		Shell:         *shell,
		WorkDir:       *workDir,
		ServerVersion: *serverVersion,
		Logger:        logger,
		Auth: agent.AuthOptions{
			AuthorizedKeys:      authorizedKeys,
			AuthorizedKeysFiles: keyFiles,
			Password:            *password,
			AllowNone:           *allowNoAuth,
		},
		SuppressNoAuthWarning: *stdio,
		RequireAuth:           !*stdio,
	}

	server, err := agent.NewServer(opts)
	if err != nil {
		return err
	}

	if *stdio {
		conn := agent.NewStdioConn(os.Stdin, os.Stdout)
		err := server.ServeConn(conn)
		if err != nil && !isClosedError(err) {
			return err
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), listenSignals()...)
	defer stop()
	return server.ListenAndServe(ctx, *listen)
}

func runProxy(args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	kubeFlags := &kubeFlagValues{}
	kubeFlags.register(fs)

	pod := fs.String("pod", os.Getenv("KUBESSH_POD"), "target pod name")
	container := fs.String("container", os.Getenv("KUBESSH_CONTAINER"), "target container name (default: pod's default container)")
	remote := fs.String("remote", envDefault("KUBESSH_REMOTE", defaultRemotePath), "path of the kubessh binary inside the container")
	agentBinary := fs.String("agent-binary", os.Getenv("KUBESSH_AGENT_BINARY"), "local kubessh binary to upload before connecting")
	uploadMode := fs.String("upload-mode", envDefault("KUBESSH_UPLOAD", "auto"), "agent install mode: auto, always, download or never")
	agentArch := fs.String("agent-arch", envDefault("KUBESSH_AGENT_ARCH", "amd64"), "linux agent architecture for downloads (amd64 or arm64)")
	downloadURL := fs.String("agent-download-url", os.Getenv("KUBESSH_AGENT_DOWNLOAD_URL"), "agent download URL template ({version}, {arch}, {os})")
	downloadTimeout := fs.Int("download-timeout", envInt("KUBESSH_DOWNLOAD_TIMEOUT", 60), "agent download timeout in seconds")
	via := fs.String("via", envDefault("KUBESSH_VIA", "exec"), "transport: exec or portforward")
	remotePort := fs.Int("port", envInt("KUBESSH_PORT", 2222), "remote agent port for --via=portforward")
	protocol := fs.String("exec-protocol", envDefault("KUBESSH_EXEC_PROTOCOL", "auto"), "exec protocol: auto, websocket or spdy")
	agentHostKey := fs.String("agent-host-key", os.Getenv("KUBESSH_AGENT_HOST_KEY"), "host key path passed to the in-container agent")
	agentShell := fs.String("agent-shell", os.Getenv("KUBESSH_AGENT_SHELL"), "shell passed to the in-container agent")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *pod == "" {
		return errors.New("--pod is required (or set KUBESSH_POD)")
	}

	if *via == "exec" && *remote == "" {
		return errors.New("--remote is required for --via=exec")
	}

	client, err := kubeFlags.client()
	if err != nil {
		return err
	}
	client.Protocol = kube.Protocol(strings.ToLower(*protocol))

	ctx, stop := signal.NotifyContext(context.Background(), proxySignals()...)
	defer stop()

	err = proxy.Run(ctx, client, proxy.Options{
		Namespace:       kubeFlags.namespace,
		Pod:             *pod,
		Container:       *container,
		Remote:          *remote,
		AgentBinary:     *agentBinary,
		UploadMode:      *uploadMode,
		Via:             *via,
		RemotePort:      *remotePort,
		Protocol:        kube.Protocol(strings.ToLower(*protocol)),
		HostKey:         *agentHostKey,
		Shell:           *agentShell,
		Version:         version,
		AgentArch:       *agentArch,
		DownloadURL:     *downloadURL,
		DownloadTimeout: time.Duration(*downloadTimeout) * time.Second,
	}, os.Stdin, os.Stdout, os.Stderr)
	if err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil) {
		return nil
	}
	return err
}

func runInject(args []string) error {
	fs := flag.NewFlagSet("inject", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	kubeFlags := &kubeFlagValues{}
	kubeFlags.register(fs)

	pod := fs.String("pod", os.Getenv("KUBESSH_POD"), "target pod name")
	container := fs.String("container", os.Getenv("KUBESSH_CONTAINER"), "target container name")
	source := fs.String("src", "", "local binary to upload")
	destination := fs.String("dest", envDefault("KUBESSH_REMOTE", defaultRemotePath), "destination path inside the container")
	protocol := fs.String("exec-protocol", envDefault("KUBESSH_EXEC_PROTOCOL", "auto"), "exec protocol: auto, websocket or spdy")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pod == "" {
		return errors.New("--pod is required (or set KUBESSH_POD)")
	}
	if *source == "" {
		return errors.New("--src is required")
	}

	client, err := kubeFlags.client()
	if err != nil {
		return err
	}
	client.Protocol = kube.Protocol(strings.ToLower(*protocol))

	ctx, stop := signal.NotifyContext(context.Background(), listenSignals()...)
	defer stop()

	if err := client.Upload(ctx, kubeFlags.namespace, *pod, *container, *source, *destination); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "kubessh: uploaded %s to %s:%s\n", *source, *pod, *destination)
	return nil
}

type kubeFlagValues struct {
	kubeconfig string
	context    string
	namespace  string
}

func (k *kubeFlagValues) register(fs *flag.FlagSet) {
	fs.StringVar(&k.kubeconfig, "kubeconfig", os.Getenv("KUBESSH_KUBECONFIG"), "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	fs.StringVar(&k.context, "context", os.Getenv("KUBESSH_CONTEXT"), "kubeconfig context")
	fs.StringVar(&k.namespace, "namespace", os.Getenv("KUBESSH_NAMESPACE"), "namespace (default from kubeconfig or \"default\")")
}

func (k *kubeFlagValues) client() (*kube.Client, error) {
	return kube.New(kube.Options{
		Kubeconfig: k.kubeconfig,
		Context:    k.context,
		Namespace:  k.namespace,
	})
}

type multiString []string

func (m *multiString) String() string { return strings.Join(*m, ",") }

func (m *multiString) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func newLogger() *log.Logger {
	return log.New(os.Stderr, "kubessh: ", log.LstdFlags)
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func isClosedError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrClosed) {
		return true
	}
	return strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed network connection")
}

func printUsage(out *os.File) {
	fmt.Fprintf(out, `kubessh - SSH into Kubernetes containers

Usage:
  kubessh agent [--stdio | --listen ADDR] [auth flags]
  kubessh proxy --pod POD [--container NAME] [kube flags] [--via exec|portforward]
  kubessh inject --pod POD --src FILE [--dest PATH] [kube flags]
  kubessh version
  kubessh hash

SSH config example:
  Host mypod
    HostName mypod
    User root
    ProxyCommand kubessh proxy --pod mypod --agent-binary /path/to/kubessh-linux-amd64
    StrictHostKeyChecking accept-new

Environment:
  KUBESSH_KUBECONFIG, KUBESSH_CONTEXT, KUBESSH_NAMESPACE, KUBESSH_POD,
  KUBESSH_CONTAINER, KUBESSH_REMOTE, KUBESSH_AGENT_BINARY, KUBESSH_UPLOAD,
  KUBESSH_AGENT_ARCH, KUBESSH_AGENT_DOWNLOAD_URL, KUBESSH_DOWNLOAD_TIMEOUT,
  KUBESSH_VIA, KUBESSH_PORT, KUBESSH_EXEC_PROTOCOL, KUBESSH_PASSWORD,
  KUBESSH_AUTHORIZED_KEYS, KUBESSH_AUTHORIZED_KEYS_FILES

Run "kubessh agent -h", "kubessh proxy -h" or "kubessh inject -h" for flags.
`)
}
