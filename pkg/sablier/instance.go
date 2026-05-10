package sablier

import (
	"strconv"
)

type InstanceStatus string

const (
	InstanceStatusStopped  InstanceStatus = "stopped"
	InstanceStatusStarting InstanceStatus = "starting"
	InstanceStatusReady    InstanceStatus = "ready"
	InstanceStatusError    InstanceStatus = "error"
)

// ProviderType identifies the infrastructure provider that manages an instance.
type ProviderType = string

const (
	ProviderDocker     ProviderType = "docker"
	ProviderSwarm      ProviderType = "swarm"
	ProviderKubernetes ProviderType = "kubernetes"
	ProviderPodman     ProviderType = "podman"
)

type InstanceInfo struct {
	Name            string                  `json:"name"`
	CurrentReplicas int32                   `json:"currentReplicas"`
	DesiredReplicas int32                   `json:"desiredReplicas"`
	Status          InstanceStatus          `json:"status"`
	Group           string                  `json:"group,omitempty"`
	Enabled         string                  `json:"enabled,omitempty"`
	Message         string                  `json:"message,omitempty"`
	Provider        ProviderType            `json:"provider,omitempty"`
	Docker          *DockerContainerInfo    `json:"docker,omitempty"`
	Swarm           *SwarmServiceInfo       `json:"swarm,omitempty"`
	Kubernetes      *KubernetesWorkloadInfo `json:"kubernetes,omitempty"`
	Podman          *PodmanContainerInfo    `json:"podman,omitempty"`

	// Priority is read from the sablier.priority label. Lower values are
	// evicted first under VRAM pressure. Defaults to DefaultPriority when the
	// label is missing or unparseable.
	Priority int `json:"priority,omitempty"`

	// PeakVRAMMB is read from the sablier.peak_vram_mb label and represents
	// the worst-case VRAM the container is expected to consume. Zero means
	// the instance does not participate in VRAM accounting (never evicted by
	// pressure, never triggers eviction).
	PeakVRAMMB uint64 `json:"peakVramMb,omitempty"`
}

// DefaultPriority is the priority assigned when the sablier.priority label is
// missing or invalid. Mid-range so unlabeled instances neither dominate nor
// get starved.
const DefaultPriority = 50

type InstanceConfiguration struct {
	Name    string
	Group   string
	Enabled string
}

func (instance InstanceInfo) IsReady() bool {
	return instance.Status == InstanceStatusReady
}

// PopulateEnabledAndGroup reads sablier.* labels from labels and writes the
// results into info. Reads sablier.enable, sablier.group, sablier.priority
// (default DefaultPriority) and sablier.peak_vram_mb (default 0).
// Centralising this logic avoids duplicating the same map lookups in every
// provider's Inspect implementation.
func PopulateEnabledAndGroup(info *InstanceInfo, labels map[string]string) {
	info.Enabled = labels["sablier.enable"]
	if info.Enabled == "true" {
		if g := labels["sablier.group"]; g != "" {
			info.Group = g
		} else {
			info.Group = "default"
		}
	}

	// Priority is only meaningful for VRAM participants. Leave it at the
	// zero value when neither label is present so existing structural
	// comparisons (e.g. provider Inspect tests) remain unaffected for
	// instances that don't opt in.
	hasPriorityLabel := false
	if raw, ok := labels["sablier.priority"]; ok {
		if v, err := strconv.Atoi(raw); err == nil {
			info.Priority = v
			hasPriorityLabel = true
		}
	}
	if raw, ok := labels["sablier.peak_vram_mb"]; ok {
		if v, err := strconv.ParseUint(raw, 10, 64); err == nil {
			info.PeakVRAMMB = v
			if !hasPriorityLabel {
				info.Priority = DefaultPriority
			}
		}
	}
}
