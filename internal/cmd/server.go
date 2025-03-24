package cmd

import (
	"time"

	"github.com/AdguardTeam/AdGuardDNSClient/internal/dnssvc"
	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/timeutil"
	"github.com/AdguardTeam/golibs/validate"
)

// serverConfig is the configuration for serving DNS requests.
type serverConfig struct {
	// BindRetry configures retrying to bind to listen addresses.
	BindRetry *bindRetryConfig `yaml:"bind_retry"`

	// ListenAddresses is the addresses server listens for requests.
	ListenAddresses []*ipPortConfig `yaml:"listen_addresses"`
}

// type check
var _ validate.Interface = (*serverConfig)(nil)

// Validate implements the [validate.Interface] interface for *serverConfig.
func (c *serverConfig) Validate() (err error) {
	if c == nil {
		return errors.ErrNoValue
	}

	var errs []error
	errs = validate.AppendSlice(errs, "listen_addresses", c.ListenAddresses)
	errs = validate.Append(errs, "bind_retry", c.BindRetry)

	return errors.Join(errs...)
}

// bindRetryConfig is the configuration for retrying to bind to listen
// addresses.
type bindRetryConfig struct {
	// Enabled enables retrying to bind to listen addresses.
	Enabled bool `yaml:"enabled"`

	// Interval is the interval to wait between retries.
	Interval timeutil.Duration `yaml:"interval"`

	// Count is the maximum number of attempts excluding the first one.
	Count uint `yaml:"count"`
}

// type check
var _ validate.Interface = (*bindRetryConfig)(nil)

// Validate implements the [validate.Interface] interface for *bindRetryConfig.
func (c *bindRetryConfig) Validate() (err error) {
	if c == nil {
		return errors.ErrNoValue
	} else if !c.Enabled {
		return nil
	}

	return validate.Positive("interval", c.Interval)
}

// toInternal converts the configuration to the internal representation.
func (c *bindRetryConfig) toInternal() (conf *dnssvc.BindRetryConfig) {
	return &dnssvc.BindRetryConfig{
		Enabled:  c.Enabled,
		Interval: time.Duration(c.Interval),
		Count:    c.Count,
	}
}
