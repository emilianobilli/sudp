package sudp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
)

/*
Config Example
{
    "server": {
		"virtual_address": 0,
		"listen": "0.0.0.0",
		"port": 7000,
		"private_key": "sdtl_private.pem"
	},
    "peers": [
		{
			"virtual_address": 1001,
			"public_key": "public.pem"
		}
	]
}
*/

type ServerConfig struct {
	VirtualAddress int    `json:"virtual_address"`
	Listen         string `json:"listen"`
	Port           int    `json:"port"`
	PrivateKey     string `json:"private_key"`
}

type PeerConfig struct {
	VirtualAddress int    `json:"virtual_address"`
	PublicKey      string `json:"public_key"`
}

type Config struct {
	Server ServerConfig `json:"server"`
	Peers  []PeerConfig `json:"peers"`
}

func ParseConfig(filePath string) (*LocalAddr, []*RemoteAddr, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	config := &Config{}
	err = decoder.Decode(config)
	if err != nil {
		return nil, nil, err
	}

	addr, e := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", config.Server.Listen, config.Server.Port))
	if e != nil {
		return nil, nil, e
	}

	priv, e := PrivateFromPemFile(config.Server.PrivateKey)
	if e != nil {
		return nil, nil, e
	}

	laddr := LocalAddr{
		VirtualAddress: uint16(config.Server.VirtualAddress),
		NetworkAddress: addr,
		PrivateKey:     priv,
	}

	raddr := []*RemoteAddr{}

	for _, peer := range config.Peers {
		pubk, e := PublicKeyFromPemFile(peer.PublicKey)
		if e != nil {
			return nil, nil, e
		}
		raddr = append(raddr, &RemoteAddr{VirtualAddress: uint16(peer.VirtualAddress), PublicKey: pubk})
	}

	return &laddr, raddr, nil
}
