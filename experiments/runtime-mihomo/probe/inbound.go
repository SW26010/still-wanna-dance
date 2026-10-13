//go:build !external_only

package main

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/metacubex/mihomo/listener/inbound"
	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/upstreamrequest"
)

// D is only an entry-path diagnostic. The listener and protocol handling are
// entirely upstream; application-owned options pin one authenticated exit.
func (r *fullRuntime) startFixedSOCKS(name string) (upstreamrequest.DialFunc, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	username, password := hex.EncodeToString(secret[:8]), hex.EncodeToString(secret[8:])
	l, err := inbound.NewSocks(&inbound.SocksOption{
		BaseOption: inbound.BaseOption{NameStr: "entry-" + name, Listen: "127.0.0.1", Port: "0", SpecialProxy: name},
		Users:      inbound.AuthUsers{{Username: username, Password: password}}, UDP: false,
	})
	if err != nil {
		return nil, err
	}
	r.listeners = append(r.listeners, l)
	if err = l.Listen(r.Tunnel); err != nil {
		return nil, err
	}
	r.privateEndpoints = append(r.privateEndpoints, endpoint{Address: l.Address(), Username: username, Password: password})
	return console.NewAuthenticatedSOCKS5Dialer(l.Address(), username, password)
}

func (r *fullRuntime) endpoints() []endpoint { return r.privateEndpoints }
