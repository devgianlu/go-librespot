package discovery

import "net"

type Service interface {
	Register(name, service, domain string, port int, txt []string, ifaces []net.Interface) error
	Shutdown()
}

var discoveryServices = map[string]Service{}

func GetService(name string) Service {
	return discoveryServices[name]
}
