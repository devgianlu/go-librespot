package zeroconf

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	avahiService         = "org.freedesktop.Avahi"
	avahiServerPath      = "/"
	avahiServerIface     = "org.freedesktop.Avahi.Server"
	avahiEntryGroupIface = "org.freedesktop.Avahi.EntryGroup"

	// Avahi constants
	avahiIfUnspec    = int32(-1) // AVAHI_IF_UNSPEC - use all interfaces
	avahiProtoUnspec = int32(-1) // AVAHI_PROTO_UNSPEC - use both IPv4 and IPv6
)

// AvahiRegistrar implements ServiceRegistrar using avahi-daemon via D-Bus.
// This allows go-librespot to share the mDNS responder with other services
// on the system instead of running its own.
//
// Compatibility: Requires avahi-daemon 0.6.x or later (uses stable D-Bus API).
// Tested with avahi 0.7 and 0.8.
type AvahiRegistrar struct {
	conn       *dbus.Conn
	entryGroup dbus.BusObject
	version    string
}

// NewAvahiRegistrar creates a new avahi-daemon service registrar.
// It connects to the system D-Bus and prepares to register services via avahi.
func NewAvahiRegistrar() (*AvahiRegistrar, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to system bus: %w", err)
	}

	// Verify avahi-daemon is available by calling GetHostName (available in all versions)
	server := conn.Object(avahiService, avahiServerPath)
	var hostname string
	err = server.Call(avahiServerIface+".GetHostName", 0).Store(&hostname)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to connect to avahi-daemon (is it running?): %w", err)
	}

	// Try to get version for logging (optional, may fail on older versions)
	version := getAvahiVersion(server)

	return &AvahiRegistrar{conn: conn, version: version}, nil
}

// getAvahiVersion attempts to retrieve the avahi-daemon version.
// Returns "unknown" if version cannot be determined.
func getAvahiVersion(server dbus.BusObject) string {
	// Try GetVersionString first (available in avahi 0.8+)
	var versionStr string
	if err := server.Call(avahiServerIface+".GetVersionString", 0).Store(&versionStr); err == nil {
		return versionStr
	}

	// Try GetAPIVersion (returns a single uint32)
	var apiVersion uint32
	if err := server.Call(avahiServerIface+".GetAPIVersion", 0).Store(&apiVersion); err == nil {
		return fmt.Sprintf("API v%d", apiVersion)
	}

	return "unknown"
}

// Version returns the avahi-daemon version string.
func (a *AvahiRegistrar) Version() string {
	return a.version
}

// Register publishes the service via avahi-daemon.
func (a *AvahiRegistrar) Register(name, serviceType, domain string, port int, txt []string) error {
	server := a.conn.Object(avahiService, avahiServerPath)

	// Create a new entry group for our service
	var groupPath dbus.ObjectPath
	err := server.Call(avahiServerIface+".EntryGroupNew", 0).Store(&groupPath)
	if err != nil {
		return fmt.Errorf("failed to create entry group: %w", err)
	}

	a.entryGroup = a.conn.Object(avahiService, groupPath)

	// Convert TXT records to [][]byte format required by avahi
	txtBytes := make([][]byte, len(txt))
	for i, t := range txt {
		txtBytes[i] = []byte(t)
	}

	// AddService signature: iiussssqaay
	// interface (i): network interface index, -1 for all
	// protocol (i): IP protocol, -1 for both IPv4/IPv6
	// flags (u): publish flags, 0 for default
	// name (s): service instance name
	// type (s): service type (e.g., "_spotify-connect._tcp")
	// domain (s): domain to publish in (e.g., "local")
	// host (s): hostname, empty for default
	// port (q): port number (uint16)
	// txt (aay): TXT record data as array of byte arrays
	err = a.entryGroup.Call(avahiEntryGroupIface+".AddService", 0,
		avahiIfUnspec,    // interface
		avahiProtoUnspec, // protocol
		uint32(0),        // flags
		name,             // service name
		serviceType,      // service type
		domain,           // domain
		"",               // host (empty = use default hostname)
		uint16(port),     // port
		txtBytes,         // TXT records
	).Err
	if err != nil {
		return fmt.Errorf("failed to add service: %w", err)
	}

	// Commit the entry group to publish the service
	err = a.entryGroup.Call(avahiEntryGroupIface+".Commit", 0).Err
	if err != nil {
		return fmt.Errorf("failed to commit entry group: %w", err)
	}

	return nil
}

// Shutdown removes the service from avahi and releases resources.
func (a *AvahiRegistrar) Shutdown() {
	if a.entryGroup != nil {
		// Free the entry group (this also unpublishes the service)
		_ = a.entryGroup.Call(avahiEntryGroupIface+".Free", 0).Err
		a.entryGroup = nil
	}
	if a.conn != nil {
		_ = a.conn.Close()
		a.conn = nil
	}
}
