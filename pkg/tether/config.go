package tether

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type linkFile struct {
	Links map[string]struct {
		Listen   string `toml:"listen"`
		Dial     string `toml:"dial"`
		Path     string `toml:"path"`
		TokenEnv string `toml:"token_env"`
	} `toml:"links"`
}

// LoadConfig reads a links file and returns exactly one of the two configs
// for link name: a server config if the link sets listen, a client config if
// it sets dial. The token is read from the environment variable token_env names.
func LoadConfig(path, name string) (*ServerConfig, *ClientConfig, error) {
	var f linkFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, nil, fmt.Errorf("tether: read %s: %w", path, err)
	}
	l, ok := f.Links[name]
	if !ok {
		return nil, nil, fmt.Errorf("tether: %s has no link %q", path, name)
	}
	switch {
	case l.Listen != "" && l.Dial != "":
		return nil, nil, fmt.Errorf("tether: link %q sets both listen and dial; set exactly one", name)
	case l.Listen == "" && l.Dial == "":
		return nil, nil, fmt.Errorf("tether: link %q sets neither listen nor dial; set exactly one", name)
	case l.TokenEnv == "":
		return nil, nil, fmt.Errorf("tether: link %q has no token_env", name)
	}
	token := os.Getenv(l.TokenEnv)
	if token == "" {
		return nil, nil, fmt.Errorf("tether: link %q: environment variable %s is empty or unset", name, l.TokenEnv)
	}
	if l.Listen != "" {
		return &ServerConfig{Addr: l.Listen, Path: l.Path, Token: token}, nil, nil
	}
	return nil, &ClientConfig{URL: l.Dial, Token: token}, nil
}
