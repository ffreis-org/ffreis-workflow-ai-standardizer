package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type TaskConfig struct {
	Name          string       `yaml:"name"`
	Description   string       `yaml:"description"`
	Model         string       `yaml:"model"`
	Context       []string     `yaml:"context"`
	SourceGlobs   []string     `yaml:"source_globs"`
	MaxDiffTokens int          `yaml:"max_diff_tokens"`
	Output        OutputConfig `yaml:"output"`
}

type OutputConfig struct {
	Type         string `yaml:"type"` // pr | issue | artifact | stdout
	BranchPrefix string `yaml:"branch_prefix"`
	SkipMarker   string `yaml:"skip_marker"`
}

func LoadTask(path string) (TaskConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TaskConfig{}, fmt.Errorf("read task config %s: %w", path, err)
	}
	var cfg TaskConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return TaskConfig{}, fmt.Errorf("parse task config %s: %w", path, err)
	}
	if cfg.Name == "" {
		return TaskConfig{}, fmt.Errorf("task config %s: missing name", path)
	}
	if len(cfg.Context) == 0 {
		return TaskConfig{}, fmt.Errorf("task config %s: context list is empty", path)
	}
	if cfg.Output.Type == "" {
		cfg.Output.Type = "stdout"
	}
	if cfg.MaxDiffTokens == 0 {
		cfg.MaxDiffTokens = 3000
	}
	return cfg, nil
}
