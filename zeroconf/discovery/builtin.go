package discovery

import (
	"net"

	"github.com/grandcat/zeroconf"
)

func init() {
	discoveryServices["builtin"] = &builtinDiscoveryService{}
}

type builtinDiscoveryService struct {
	server *zeroconf.Server
}

func (s *builtinDiscoveryService) Register(name, service, domain string, port int, txt []string, ifaces []net.Interface) (err error) {
	s.server, err = zeroconf.Register(name, service, domain, port, txt, ifaces)
	if err != nil {
		return err
	}
	return nil
}

func (s *builtinDiscoveryService) Shutdown() {
	if s.server != nil {
		s.server.Shutdown()
	}
}
