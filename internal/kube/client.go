package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const inClusterNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// Options configures how the Kubernetes client is built.
type Options struct {
	Kubeconfig string
	Context    string
	Namespace  string
}

// Client wraps a Kubernetes clientset together with the REST configuration
// needed for exec and port-forward subresources.
type Client struct {
	Clientset kubernetes.Interface
	Config    *rest.Config
	Namespace string
	Protocol  Protocol
}

// New loads a kubeconfig (or in-cluster configuration) and returns a client.
func New(opts Options) (*Client, error) {
	if opts.Kubeconfig == "" {
		opts.Kubeconfig = os.Getenv("KUBESSH_KUBECONFIG")
	}
	if opts.Context == "" {
		opts.Context = os.Getenv("KUBESSH_CONTEXT")
	}
	if opts.Namespace == "" {
		opts.Namespace = os.Getenv("KUBESSH_NAMESPACE")
	}
	if opts.Namespace == "" {
		opts.Namespace = os.Getenv("POD_NAMESPACE")
	}

	cfg, namespace, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}
	if opts.Namespace != "" && namespace == "" {
		namespace = opts.Namespace
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	return &Client{Clientset: clientset, Config: cfg, Namespace: namespace, Protocol: ProtocolAuto}, nil
}

func buildConfig(opts Options) (*rest.Config, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loadingRules.ExplicitPath = opts.Kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if opts.Context != "" {
		overrides.CurrentContext = opts.Context
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	cfg, clientErr := clientConfig.ClientConfig()
	if clientErr == nil {
		namespace := opts.Namespace
		if namespace == "" {
			if ns, _, err := clientConfig.Namespace(); err == nil {
				namespace = ns
			}
		}
		if namespace == "" {
			namespace = "default"
		}
		return cfg, namespace, nil
	}

	inClusterCfg, inClusterErr := rest.InClusterConfig()
	if inClusterErr != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", clientErr)
	}

	namespace := opts.Namespace
	if namespace == "" {
		namespace = inClusterNamespace()
	}
	if namespace == "" {
		namespace = "default"
	}
	return inClusterCfg, namespace, nil
}

func (c *Client) protocols() []Protocol {
	switch c.Protocol {
	case ProtocolWebSocket:
		return []Protocol{ProtocolWebSocket}
	case ProtocolSPDY:
		return []Protocol{ProtocolSPDY}
	default:
		return []Protocol{ProtocolWebSocket, ProtocolSPDY}
	}
}

func inClusterNamespace() string {
	if data, err := os.ReadFile(filepath.Clean(inClusterNamespacePath)); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}
