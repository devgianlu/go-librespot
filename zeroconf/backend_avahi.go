package zeroconf

import (
	"fmt"
	"sync"

	librespot "github.com/devgianlu/go-librespot"
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

	// AvahiServerState values (see avahi-common/defs.h)
	avahiServerInvalid     = int32(0) // AVAHI_SERVER_INVALID
	avahiServerRegistering = int32(1) // AVAHI_SERVER_REGISTERING - host name being registered
	avahiServerRunning     = int32(2) // AVAHI_SERVER_RUNNING - host name registered, services may be published
	avahiServerCollision   = int32(3) // AVAHI_SERVER_COLLISION - host name collision, being renamed
	avahiServerFailure     = int32(4) // AVAHI_SERVER_FAILURE

	// AvahiEntryGroupState values (see avahi-common/defs.h)
	avahiEntryGroupUncommited  = int32(0) // AVAHI_ENTRY_GROUP_UNCOMMITED
	avahiEntryGroupRegistering = int32(1) // AVAHI_ENTRY_GROUP_REGISTERING
	avahiEntryGroupEstablished = int32(2) // AVAHI_ENTRY_GROUP_ESTABLISHED
	avahiEntryGroupCollision   = int32(3) // AVAHI_ENTRY_GROUP_COLLISION - service name collision
	avahiEntryGroupFailure     = int32(4) // AVAHI_ENTRY_GROUP_FAILURE
)

// AvahiRegistrar implements ServiceRegistrar using avahi-daemon via D-Bus.
// This allows go-librespot to share the mDNS responder with other services
// on the system instead of running its own.
//
// It follows the recommended avahi client pattern of watching for state
// changes: service name collisions are resolved by picking an alternative
// name, and host name changes (e.g. caused by a host name collision on the
// network) cause the service to be transparently re-published once the
// daemon settles. See the canonical avahi example client-publish-service.c.
//
// Compatibility: Requires avahi-daemon 0.6.x or later (uses stable D-Bus API).
// Tested with avahi 0.7 and 0.8.
type AvahiRegistrar struct {
	log     librespot.Logger
	conn    *dbus.Conn
	version string

	server  dbus.BusObject
	signals chan *dbus.Signal
	done    chan struct{}
	wg      sync.WaitGroup

	// mu guards all the mutable state below, which is accessed both from the
	// caller (Register/UpdateName/Shutdown) and from the signal goroutine.
	mu         sync.Mutex
	closed     bool
	registered bool // whether the caller has requested the service to be published

	entryGroup dbus.BusObject
	groupPath  dbus.ObjectPath

	requestedName string // name as requested by the caller (reset point for collisions)
	name          string // name currently advertised (may differ after a collision)
	serviceType   string
	domain        string
	port          int
	txt           []string
}

// NewAvahiRegistrar creates a new avahi-daemon service registrar.
// It connects to the system D-Bus and prepares to register services via avahi.
func NewAvahiRegistrar(log librespot.Logger) (*AvahiRegistrar, error) {
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

	a := &AvahiRegistrar{
		log:     log,
		conn:    conn,
		version: version,
		server:  server,
		signals: make(chan *dbus.Signal, 16),
		done:    make(chan struct{}),
	}

	// Subscribe to Server and EntryGroup state changes so we can react to host
	// name changes and service name collisions. The entry group path is only
	// known after EntryGroupNew, so we match the interface/member and filter by
	// path in the handler.
	matchOk := true
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(avahiServerIface),
		dbus.WithMatchMember("StateChanged"),
		dbus.WithMatchObjectPath(avahiServerPath),
	); err != nil {
		log.WithError(err).Warnf("failed subscribing to avahi server state changes, host name changes will not be handled")
		matchOk = false
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(avahiEntryGroupIface),
		dbus.WithMatchMember("StateChanged"),
	); err != nil {
		log.WithError(err).Warnf("failed subscribing to avahi entry group state changes, service name collisions will not be handled")
		matchOk = false
	}

	if matchOk {
		conn.Signal(a.signals)
		a.wg.Add(1)
		go a.watchSignals()
	}

	return a, nil
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
	a.mu.Lock()
	defer a.mu.Unlock()

	a.registered = true
	a.requestedName = name
	a.name = name
	a.serviceType = serviceType
	a.domain = domain
	a.port = port
	a.txt = txt

	return a.publishLocked()
}

// UpdateName updates the advertised instance service name.
func (a *AvahiRegistrar) UpdateName(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.log.Debugf("avahi service name update requested: %q -> %q", a.name, name)

	a.requestedName = name
	a.name = name

	return a.publishLocked()
}

// publishLocked (re)publishes the service using the current state. It creates
// the entry group if necessary, then resets, re-adds and commits the service.
// Callers must hold a.mu.
func (a *AvahiRegistrar) publishLocked() error {
	if a.closed || !a.registered {
		return nil
	}

	// Create a new entry group for our service if we don't have one yet.
	if a.entryGroup == nil {
		var groupPath dbus.ObjectPath
		if err := a.server.Call(avahiServerIface+".EntryGroupNew", 0).Store(&groupPath); err != nil {
			return fmt.Errorf("failed to create entry group: %w", err)
		}

		a.entryGroup = a.conn.Object(avahiService, groupPath)
		a.groupPath = groupPath
	} else {
		// Reset any previously committed entries so we can re-add the service.
		// A committed group cannot be modified, so this is required both for
		// name changes and when re-publishing after a host name change.
		_ = a.entryGroup.Call(avahiEntryGroupIface+".Reset", 0).Err
	}

	// Convert TXT records to [][]byte format required by avahi
	txtBytes := make([][]byte, len(a.txt))
	for i, t := range a.txt {
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
	if err := a.entryGroup.Call(avahiEntryGroupIface+".AddService", 0,
		avahiIfUnspec,    // interface
		avahiProtoUnspec, // protocol
		uint32(0),        // flags
		a.name,           // service name
		a.serviceType,    // service type
		a.domain,         // domain
		"",               // host (empty = use default hostname)
		uint16(a.port),   // port
		txtBytes,         // TXT records
	).Err; err != nil {
		return fmt.Errorf("failed to add service: %w", err)
	}

	// Commit the entry group to publish the service
	if err := a.entryGroup.Call(avahiEntryGroupIface+".Commit", 0).Err; err != nil {
		return fmt.Errorf("failed to commit entry group: %w", err)
	}

	return nil
}

// watchSignals dispatches avahi D-Bus signals until Shutdown is called.
func (a *AvahiRegistrar) watchSignals() {
	defer a.wg.Done()

	for {
		select {
		case <-a.done:
			return
		case sig, ok := <-a.signals:
			if !ok {
				return
			}
			a.handleSignal(sig)
		}
	}
}

func (a *AvahiRegistrar) handleSignal(sig *dbus.Signal) {
	if len(sig.Body) < 1 {
		return
	}
	state, ok := sig.Body[0].(int32)
	if !ok {
		return
	}

	switch sig.Name {
	case avahiServerIface + ".StateChanged":
		a.handleServerStateChanged(state)
	case avahiEntryGroupIface + ".StateChanged":
		a.handleGroupStateChanged(sig.Path, state)
	}
}

// handleServerStateChanged reacts to host name registration changes. Once the
// server is running again (after a host name change) the service is re-published
// so it picks up the new host name.
func (a *AvahiRegistrar) handleServerStateChanged(state int32) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch state {
	case avahiServerRunning:
		if !a.registered {
			return
		}
		a.log.Debugf("avahi server running, (re)publishing service %q", a.name)
		if err := a.publishLocked(); err != nil {
			a.log.WithError(err).Errorf("failed (re)publishing avahi service after server became running")
		}
	case avahiServerRegistering, avahiServerCollision:
		a.log.Debugf("avahi host name changing (state %d)", state)
	case avahiServerFailure:
		a.log.Errorf("avahi server entered failure state")
	case avahiServerInvalid:
	}
}

// handleGroupStateChanged reacts to service registration changes, most notably
// service name collisions, which are resolved by picking an alternative name.
func (a *AvahiRegistrar) handleGroupStateChanged(path dbus.ObjectPath, state int32) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Ignore signals for entry groups that are not ours.
	if a.entryGroup == nil || path != a.groupPath {
		return
	}

	switch state {
	case avahiEntryGroupCollision:
		alt, err := a.alternativeNameLocked(a.name)
		if err != nil {
			a.log.WithError(err).Errorf("failed obtaining alternative name after avahi service name collision for %q", a.name)
			return
		}

		a.log.Warnf("avahi service name collision, renaming %q -> %q", a.name, alt)
		a.name = alt
		if err := a.publishLocked(); err != nil {
			a.log.WithError(err).Errorf("failed re-publishing avahi service as %q after collision", alt)
		}
	case avahiEntryGroupFailure:
		a.log.Errorf("avahi entry group entered failure state for service %q", a.name)
	case avahiEntryGroupEstablished:
		a.log.Debugf("avahi service %q established", a.name)
	case avahiEntryGroupUncommited, avahiEntryGroupRegistering:
	}
}

// alternativeNameLocked asks avahi for the next alternative service name to use
// after a collision (e.g. "name" -> "name #2"). Callers must hold a.mu.
func (a *AvahiRegistrar) alternativeNameLocked(name string) (string, error) {
	var alt string
	if err := a.server.Call(avahiServerIface+".GetAlternativeServiceName", 0, name).Store(&alt); err != nil {
		return "", err
	}
	return alt, nil
}

// Shutdown removes the service from avahi and releases resources.
func (a *AvahiRegistrar) Shutdown() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	a.registered = false
	if a.entryGroup != nil {
		// Free the entry group (this also unpublishes the service)
		_ = a.entryGroup.Call(avahiEntryGroupIface+".Free", 0).Err
		a.entryGroup = nil
	}
	a.mu.Unlock()

	// Stop the signal goroutine before tearing down the connection.
	close(a.done)
	if a.signals != nil {
		a.conn.RemoveSignal(a.signals)
	}
	a.wg.Wait()

	if a.conn != nil {
		_ = a.conn.Close()
		a.conn = nil
	}
}
