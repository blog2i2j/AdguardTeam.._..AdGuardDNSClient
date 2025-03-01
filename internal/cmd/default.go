package cmd

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/AdguardTeam/AdGuardDNSClient/internal/agdc"
	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/logutil/slogutil"
	"github.com/AdguardTeam/golibs/netutil"
	"github.com/AdguardTeam/golibs/timeutil"
	"github.com/c2h5oh/datasize"
	"gopkg.in/yaml.v3"
)

const (
	// defaultPlainDNSPort is the default port for plain DNS.
	defaultPlainDNSPort uint16 = 53

	// defaultUpstreamTimeout is the default timeout for outgoing DNS requests.
	defaultUpstreamTimeout = 2 * time.Second
)

// filterInterfaceAddrs gets the addresses as given by [net.InterfaceAddrs] and
// filters out the ones that are not in the set.  It returns the [ipPortConfig]s
// for the eligible addresses created using port p.
func filterInterfaceAddrs(
	addrs []net.Addr,
	set netutil.SubnetSet,
	p uint16,
) (confs []*ipPortConfig) {
	for _, a := range addrs {
		addrStr := a.String()
		pref, err := netip.ParsePrefix(addrStr)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "unexpected %q format: %s\n", addrStr, err)

			continue
		}

		addr := pref.Addr()
		if !set.Contains(addr) {
			_, _ = fmt.Fprintf(os.Stderr, "can not listen on %s\n", addr)

			continue
		}

		confs = append(confs, &ipPortConfig{
			Address: netip.AddrPortFrom(addr, p),
		})

		_, _ = fmt.Fprintf(os.Stderr, "adding %s to default listening addresses\n", addr)
	}

	return confs
}

// isListenable returns true if the address is not a link-local unicast address
// and is served locally.
func isListenable(addr netip.Addr) (ok bool) {
	return addr.IsPrivate() || addr.IsLoopback()
}

// allListenableAddresses returns all the addresses of network interfaces that
// are local and are not link-local unicast addresses.
func allListenableAddresses() (laddrs []*ipPortConfig, err error) {
	netAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("getting interfaces addresses: %w", err)
	}

	set := netutil.SubnetSetFunc(isListenable)

	return filterInterfaceAddrs(netAddrs, set, defaultPlainDNSPort), nil
}

// newDefaultServerConfig creates a new server configuration with the local
// addresses of the machine.
func newDefaultServerConfig() (c *serverConfig, err error) {
	defer func() { err = errors.Annotate(err, "creating default server configuration: %w") }()

	localAddrs, err := allListenableAddresses()
	if err != nil {
		// Don't wrap the error since there is already an annotation deferred.
		return nil, err
	}

	return &serverConfig{
		ListenAddresses: localAddrs,
	}, nil
}

// newDefaultDNSConfig creates a new default configuration for DNS.
func newDefaultDNSConfig() (c *dnsConfig, err error) {
	defer func() { err = errors.Annotate(err, "creating default DNS configuration: %w") }()

	serverConf, err := newDefaultServerConfig()
	if err != nil {
		// Don't wrap the error since there is already an annotation deferred.
		return nil, err
	}

	bootstrapServers := []*ipPortConfig{{
		Address: netip.AddrPortFrom(netip.MustParseAddr("9.9.9.10"), defaultPlainDNSPort),
	}, {
		Address: netip.AddrPortFrom(netip.MustParseAddr("149.112.112.10"), defaultPlainDNSPort),
	}, {
		Address: netip.AddrPortFrom(netip.MustParseAddr("2620:fe::10"), defaultPlainDNSPort),
	}, {
		Address: netip.AddrPortFrom(netip.MustParseAddr("2620:fe::fe:10"), defaultPlainDNSPort),
	}}
	upstreamGroups := upstreamGroupsConfig{
		agdc.UpstreamGroupNameDefault: &upstreamGroupConfig{
			Address: "https://unfiltered.adguard-dns.com/dns-query",
			// TODO(e.burkov):  It marshals into an empty slice, but should not
			// appear in the configuration file at all.
			Match: nil,
		},
	}
	fallbackServers := []*urlConfig{{
		Address: "tls://94.140.14.140",
	}}

	return &dnsConfig{
		Server: serverConf,
		Cache: &cacheConfig{
			Enabled:    true,
			Size:       128 * datasize.MB,
			ClientSize: 4 * datasize.MB,
		},
		Bootstrap: &bootstrapConfig{
			Servers: bootstrapServers,
			Timeout: timeutil.Duration(defaultUpstreamTimeout),
		},
		Upstream: &upstreamConfig{
			Groups:  upstreamGroups,
			Timeout: timeutil.Duration(defaultUpstreamTimeout),
		},
		Fallback: &fallbackConfig{
			Servers: fallbackServers,
			Timeout: timeutil.Duration(defaultUpstreamTimeout),
		},
	}, nil
}

// newDefaultConfig creates a new ready-to-use default configuration for a newly
// installed service.
func newDefaultConfig() (c *configuration, err error) {
	defer func() { err = errors.Annotate(err, "creating default configuration: %w") }()

	dnsConf, err := newDefaultDNSConfig()
	if err != nil {
		// Don't wrap the error since there is already an annotation deferred.
		return nil, err
	}

	return &configuration{
		DNS: dnsConf,
		Debug: &debugConfig{
			Pprof: &pprofConfig{
				Enabled: false,
				Port:    6060,
			},
		},
		Log: &logConfig{
			Output:    outputSyslog,
			Format:    slogutil.FormatDefault,
			Timestamp: false,
			Verbose:   false,
		},
		SchemaVersion: currentSchemaVersion,
	}, nil
}

// writeDefaultConfig writes the default configuration to the file at path.  If
// the file at path already exists, it does nothing.
func writeDefaultConfig(path string) (err error) {
	defer func() { err = errors.Annotate(err, "writing default configuration: %w") }()

	// #nosec G304 -- Trust the path to the configuration file that is currently
	// expected to be in the same directory as the binary.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// TODO(e.burkov):  Log properly.
			_, _ = fmt.Fprintf(os.Stderr, "using configuration file %q\n", path)

			return nil
		}

		return fmt.Errorf("creating configuration file: %w", err)
	}
	defer func() { err = errors.WithDeferred(err, f.Close()) }()

	_, _ = fmt.Fprintln(os.Stderr, "creating default configuration")

	conf, err := newDefaultConfig()
	if err != nil {
		// Don't wrap the error since it's informative enough as is.
		return err
	}

	_, _ = fmt.Fprintln(os.Stderr, "writing default configuration")

	return yaml.NewEncoder(f).Encode(conf)
}
