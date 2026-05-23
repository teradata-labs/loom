package loadtest

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// EnvironmentInfo captures the runtime and infrastructure environment
// for reproducibility of benchmark results.
type EnvironmentInfo struct {
	GoVersion    string `json:"go_version"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	NumCPU       int    `json:"num_cpu"`
	GOMAXPROCS   int    `json:"gomaxprocs"`
	GRPCVersion  string `json:"grpc_version"`
	CommitSHA    string `json:"commit_sha"`
	RaceDetector bool   `json:"race_detector"`
	TimestampUTC string `json:"timestamp_utc"`

	// K8s metadata (populated from downward API env vars if running in a pod)
	NodeName   string `json:"node_name,omitempty"`
	PodName    string `json:"pod_name,omitempty"`
	K8sVersion string `json:"k8s_version,omitempty"`
	VMSize     string `json:"vm_size,omitempty"`
	Region     string `json:"region,omitempty"`

	// System info (populated on Linux)
	KernelVersion string `json:"kernel_version,omitempty"`
	CPUModel      string `json:"cpu_model,omitempty"`
	RAMTotalMB    int64  `json:"ram_total_mb,omitempty"`
}

// CaptureEnvironment gathers runtime and system information.
func CaptureEnvironment() EnvironmentInfo {
	env := EnvironmentInfo{
		GoVersion:    runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		GOMAXPROCS:   runtime.GOMAXPROCS(0),
		GRPCVersion:  "v1.79.3", // from go.mod
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
	}

	// Commit SHA: prefer env var (set by Docker build), fall back to git
	if sha := os.Getenv("GIT_COMMIT"); sha != "" {
		env.CommitSHA = sha
	} else if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		env.CommitSHA = strings.TrimSpace(string(out))
	}

	// K8s downward API
	env.NodeName = os.Getenv("NODE_NAME")
	env.PodName = os.Getenv("POD_NAME")
	env.K8sVersion = os.Getenv("K8S_VERSION")
	env.VMSize = os.Getenv("VM_SIZE")
	env.Region = os.Getenv("BENCH_LOCATION")

	// System info (best-effort, Linux only)
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("uname", "-r").Output(); err == nil {
			env.KernelVersion = strings.TrimSpace(string(out))
		}
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "model name") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						env.CPUModel = strings.TrimSpace(parts[1])
					}
					break
				}
			}
		}
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					// Parse "MemTotal:       131891724 kB"
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						var kb int64
						for _, c := range fields[1] {
							if c >= '0' && c <= '9' {
								kb = kb*10 + int64(c-'0')
							}
						}
						env.RAMTotalMB = kb / 1024
					}
					break
				}
			}
		}
	}

	return env
}
