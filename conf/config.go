package conf

type ConfigYaml struct {
	Listen   ListenYaml   `yaml:"listen"`
	DBPath   string       `yaml:"DBPath"`
	Identity IdentityYaml `yaml:"identity"`
}
