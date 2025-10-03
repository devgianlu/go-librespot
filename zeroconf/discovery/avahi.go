//go:build linux

package discovery

import (
	"fmt"
	"net"

	"github.com/OpenPrinting/go-avahi"
)

func init() {
	discoveryServices["avahi"] = &avahiDiscoveryService{}
}

type avahiDiscoveryService struct {
	client *avahi.Client
	group  *avahi.EntryGroup
}

func (s *avahiDiscoveryService) Register(name, service, domain string, port int, txt []string, ifaces []net.Interface) (err error) {
	if len(ifaces) > 0 {
		return fmt.Errorf("avahi discovery does not support specifying interfaces")
	}

	s.client, err = avahi.NewClient(avahi.ClientLoopbackWorkarounds)
	if err != nil {
		return fmt.Errorf("failed to create Avahi client: %w", err)
	}

	s.group, err = avahi.NewEntryGroup(s.client)
	if err != nil {
		return fmt.Errorf("failed to create Avahi entry group: %w", err)
	}

	if err = s.group.AddService(&avahi.EntryGroupService{
		IfIdx:        avahi.IfIndexUnspec,
		Proto:        avahi.ProtocolUnspec,
		InstanceName: name,
		SvcType:      service,
		Domain:       domain,
		Port:         port,
		Txt:          txt,
	}, 0); err != nil {
		return fmt.Errorf("failed to add service to Avahi entry group: %w", err)
	}

	if err = s.group.Commit(); err != nil {
		return fmt.Errorf("failed to commit Avahi entry group: %w", err)
	}

	return nil
}

func (s *avahiDiscoveryService) Shutdown() {
	s.group.Close()
	s.client.Close()
}
