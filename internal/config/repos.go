package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type ReposConfig struct {
	Repos []RepoEntry `yaml:"repos"`
}

type RepoEntry struct {
	Repo          string   `yaml:"repo"` // "owner/name"
	Tasks         []string `yaml:"tasks"`
	ModelOverride string   `yaml:"model_override"` // optional
	Branch        string   `yaml:"branch"`         // default branch, optional (default: main)
}

func LoadRepos(path string) (ReposConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ReposConfig{}, fmt.Errorf("read repos config %s: %w", path, err)
	}
	var cfg ReposConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ReposConfig{}, fmt.Errorf("parse repos config %s: %w", path, err)
	}
	for i, r := range cfg.Repos {
		if r.Repo == "" {
			return ReposConfig{}, fmt.Errorf("repos[%d]: missing repo", i)
		}
		if len(r.Tasks) == 0 {
			return ReposConfig{}, fmt.Errorf("repos[%d] (%s): no tasks assigned", i, r.Repo)
		}
		if cfg.Repos[i].Branch == "" {
			cfg.Repos[i].Branch = "main"
		}
	}
	return cfg, nil
}
