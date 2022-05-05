package rkgrpcweb

import "time"

type Websockets struct {
	PingInterval time.Duration `yaml:"pingInterval" json:"ping_interval"`
	ReadLimit    int64         `yaml:"readLimit" json:"read_limit"`
}
