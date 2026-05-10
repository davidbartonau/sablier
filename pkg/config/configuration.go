package config

type Config struct {
	Server   Server
	Storage  Storage
	Provider Provider
	Sessions Sessions
	Logging  Logging
	Strategy Strategy
	VRAM     VRAM
}

func NewConfig() Config {
	return Config{
		Server:   NewServerConfig(),
		Storage:  NewStorageConfig(),
		Provider: NewProviderConfig(),
		Sessions: NewSessionsConfig(),
		Logging:  NewLoggingConfig(),
		Strategy: NewStrategyConfig(),
		VRAM:     NewVRAMConfig(),
	}
}
