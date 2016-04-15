package pontoon

const (
	// kubernetesAPIServer = "192.168.1.200:8080"
	KubernetesAPIServer = "192.168.15.150:8080"
	RunningInKubernetes = true
	PORT                = ":55123"
)

var ValidPorts = []string{":55125", ":55126", ":55127", ":55128", ":55129", ":55130"}
