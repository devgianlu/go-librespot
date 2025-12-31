package zeroconf

// ServiceRegistrar handles mDNS service registration.
// Implementations can use different backends like built-in mDNS or avahi-daemon.
type ServiceRegistrar interface {
	// Register publishes the service via mDNS.
	// name: service instance name (e.g., "go-librespot")
	// serviceType: service type (e.g., "_spotify-connect._tcp")
	// domain: domain to register in (e.g., "local.")
	// port: TCP port the service is listening on
	// txt: TXT record key=value pairs
	Register(name, serviceType, domain string, port int, txt []string) error

	// Shutdown stops advertising the service and releases resources.
	Shutdown()
}
