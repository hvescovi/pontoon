package pontoon

const (
	//KubernetesAPIServer = "192.168.1.200:8080"
	KubernetesAPIServer = "172.18.0.10:8080"

  // if running as local processes (outside containers), set is false
	RunningInKubernetes = true

	// port of
	PORT                = ":55123"
	APP_PORT            = ":65432"
	ASCII_CHARS         = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	PAYLOAD_SIZE        = 100

)
