package conf

import (
	"strings"
	"time"
)

type ListenYaml struct {
	Web           string        `yaml:"web"`
	ReadTimeout   time.Duration `yaml:"readTimeout"`
	WriteTimeout  time.Duration `yaml:"writeTimeout"`
	Domains       []string      `yaml:"domains"`
	BasePrefixURL string        `yaml:"basePrefixURL"`
	ReadLimit     int           `yaml:"readLimit"`
}

func (ly ListenYaml) GetReadTimeout() time.Duration {
	if (ly.ReadTimeout.Seconds() < 1 && ly.ReadTimeout > 0) || ly.ReadTimeout < 0 {
		return time.Second
	} else {
		return ly.ReadTimeout
	}
}

func (ly ListenYaml) GetWriteTimeout() time.Duration {
	if (ly.WriteTimeout.Seconds() < 1 && ly.WriteTimeout > 0) || ly.WriteTimeout < 0 {
		return time.Second
	} else {
		return ly.WriteTimeout
	}
}

func (ly ListenYaml) GetBasePrefixURL() string {
	bpURL := ly.BasePrefixURL
	if !strings.HasPrefix(bpURL, "/") {
		bpURL = "/" + bpURL
	}
	if strings.HasSuffix(bpURL, "/") {
		return bpURL
	} else {
		return bpURL + "/"
	}
}

func (ly ListenYaml) GetReadLimit() int64 {
	if ly.ReadLimit < 1024 {
		return 1024
	}
	return int64(ly.ReadLimit)
}
