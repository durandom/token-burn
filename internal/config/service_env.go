package config

import (
	"fmt"
	"sort"
)

// ApplyServiceEnv applies config defaults once at command startup. A variable
// already present in the process environment wins, including an empty value.
// Errors identify only the key; setter errors may contain secret values.
func ApplyServiceEnv(cfg Config, lookup func(string) (string, bool), set func(string, string) error) error {
	keys := make([]string, 0, len(cfg.Service.Env))
	for key := range cfg.Service.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := lookup(key); exists {
			continue
		}
		if err := set(key, cfg.Service.Env[key]); err != nil {
			return fmt.Errorf("set service.env %q failed", key)
		}
	}
	return nil
}
