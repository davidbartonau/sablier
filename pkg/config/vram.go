package config

// VRAM configures memory-pressure-aware eviction. When Enabled, Sablier
// evicts loaded instances on demand (only when an incoming request needs
// VRAM that isn't available) using a priority + recency policy.
//
// Containers opt in via the sablier.peak_vram_mb label. Containers without
// that label do not participate: they are never evicted by pressure and
// never trigger eviction.
type VRAM struct {
	// Enabled turns VRAM-aware eviction on. Default false preserves existing
	// time-only eviction behaviour.
	Enabled bool `mapstructure:"ENABLED" yaml:"enabled" default:"false"`

	// TotalMB is the total VRAM budget Sablier accounts against. Typically
	// the GPU's total memory, possibly minus an external reservation if
	// other processes share the device. Required when Enabled.
	TotalMB uint64 `mapstructure:"TOTAL_MB" yaml:"totalMB"`

	// HeadroomMB is the always-free buffer kept above the eviction
	// threshold. A new request needing N MB triggers eviction when
	// available is below N + HeadroomMB.
	HeadroomMB uint64 `mapstructure:"HEADROOM_MB" yaml:"headroomMB"`
}

func NewVRAMConfig() VRAM {
	return VRAM{
		Enabled:    false,
		TotalMB:    0,
		HeadroomMB: 0,
	}
}
