package m

import (
	"net/url"
	"time"
)

var (
	MalIndexFileName  = "mals.yaml"
	ManifestFileName  = "mal.yaml"
	DefaultMalName    = "Default"
	DefaultMalRepoURL = "https://api.github.com/repos/chainreactors/mals/releases"

	DefaultMalConfig = &MalConfig{
		//PublicKey: DefaultArmoryPublicKey,
		RepoURL: DefaultMalRepoURL,
		Name:    DefaultMalName,
		Enabled: true,
	}

	//malIndexSigFileName = "mal.minisig"
)

type MalsYaml struct {
	Mals []*MalConfig `yaml:"mals"`
}

type MalConfig struct {
	RepoURL          string `yaml:"repo_url"`
	Authorization    string `yaml:"authorization"`
	AuthorizationCmd string `yaml:"authorization_cmd"`
	Name             string `yaml:"name"`
	Enabled          bool   `yaml:"enabled"`
	Version          string `yaml:"version"`
	Help             string `yaml:"help"`
}

// MalHTTPConfig - Configuration for armory HTTP client
type MalHTTPConfig struct {
	MalConfig            *MalConfig
	IgnoreCache          bool
	ProxyURL             *url.URL
	Timeout              time.Duration
	DisableTLSValidation bool
}
