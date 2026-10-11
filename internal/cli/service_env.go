package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/durandom/token-burn/internal/config"
)

func loadRuntimeConfig(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	if err := config.ApplyServiceEnv(cfg, os.LookupEnv, os.Setenv); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func warnServiceEnvPermissions(w io.Writer, path string, cfg config.Config) {
	if cfg.Service.Env == nil {
		return
	}
	if path == "" {
		path = config.DefaultPath()
	}
	path, err := config.ExpandPath(path)
	if err != nil {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o044 == 0 {
		return
	}
	fmt.Fprintf(w, "warning: config %q has mode %04o; [service.env] secrets are group/world readable\n", path, info.Mode().Perm())
}
