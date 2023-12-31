package conf

import (
	"net/url"
	"strconv"
)

type IdentityYaml struct {
	ID           uint32 `yaml:"id"`
	PublicKey    string `yaml:"publicKey"`
	PublicKeyURL string `yaml:"publicKeyURL"`
}

func (i IdentityYaml) GetPublicKeyURL() string {
	tURL, err := url.Parse(i.PublicKeyURL)
	if err != nil {
		return ""
	}
	return tURL.String()
}

func (i IdentityYaml) GetID() string {
	return strconv.Itoa(int(i.ID))
}
