package config

import "github.com/spf13/viper"

// Config holds application configuration loaded from environment variables.
// Environment variables are prefixed with AGENT_LOGS_EXTRACTOR_
// (e.g. AGENT_LOGS_EXTRACTOR_LOG_LEVEL=debug).
//
// Add fields here as your application grows and bind them in Load().
type Config struct {
	LogLevel string `mapstructure:"log_level"`
}

// Load returns the current configuration from Viper.
// Assumes viper.SetEnvPrefix and viper.AutomaticEnv have already been called
// in the root command setup.
func Load() (*Config, error) {
	viper.SetDefault("log_level", "info")

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
