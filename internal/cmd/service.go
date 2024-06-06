package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AdguardTeam/AdGuardDNSClient/internal/dnssvc"
	"github.com/AdguardTeam/AdGuardDNSClient/internal/version"
	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/logutil/slogutil"
	"github.com/AdguardTeam/golibs/service"
	osservice "github.com/kardianos/service"
)

// newDefaultService creates a new [osservice.Service] instance for the current
// system according to opts.
func newDefaultService(opts *options) (svc osservice.Service, confPath string, err error) {
	sys := osservice.ChosenSystem()
	if sys == nil {
		return nil, "", errors.ErrUnsupported
	}

	// TODO(e.burkov):  Use -c command-line flag to specify the configuration
	// file instead of using the default one from the executable's directory.

	execPath, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("getting executable path: %w", err)
	}

	absExecPath, err := filepath.Abs(execPath)
	if err != nil {
		return nil, "", fmt.Errorf("getting absolute executable path: %w", err)
	}

	confPath = filepath.Join(filepath.Dir(absExecPath), defaultConfigPath)

	defaulSvc := &defaultService{
		opts:     opts,
		done:     make(chan struct{}),
		errCh:    make(chan error),
		confPath: confPath,
	}

	svc, err = sys.New(defaulSvc, newServiceConfig())
	if err != nil {
		return nil, confPath, fmt.Errorf("creating service: %w", err)
	}

	return svc, confPath, nil
}

// newServiceConfig creates a configuration that the OS service manager uses to
// control the service.
func newServiceConfig() (conf *osservice.Config) {
	return &osservice.Config{
		Name:        "AdGuardDNSClient",
		DisplayName: "AdGuardDNS Client",
		Description: "A DNS client for AdGuardDNS",
	}
}

// serviceAction is a type for service actions that only allows a predefined set
// of values.
type serviceAction string

// Available service commands.
const (
	serviceActionInstall   serviceAction = "install"
	serviceActionRestart   serviceAction = "restart"
	serviceActionStart     serviceAction = "start"
	serviceActionStatus    serviceAction = "status"
	serviceActionStop      serviceAction = "stop"
	serviceActionUninstall serviceAction = "uninstall"
)

// type check
var _ flag.Value = (*serviceAction)(nil)

// Set implements the [flag.Value] interface for [*serviceAction].
func (a *serviceAction) Set(value string) (err error) {
	switch sa := serviceAction(value); sa {
	case
		serviceActionInstall,
		serviceActionRestart,
		serviceActionStart,
		serviceActionStatus,
		serviceActionStop,
		serviceActionUninstall:
		*a = sa

		return nil
	default:
		return errUnknownAction
	}
}

// String implements the [flag.Value] interface for [serviceAction].
func (a serviceAction) String() (s string) { return string(a) }

// control performs the specified service action.  It mirrors the service logic
// from [service.Control], but returns better errors.
func control(svc osservice.Service, confPath string, action serviceAction) (err error) {
	defer func() { err = errors.Annotate(err, "performing %q: %w", action) }()

	// TODO(e.burkov):  Use common logger.
	var l osservice.Logger
	l, err = svc.Logger(nil)
	if err != nil {
		return fmt.Errorf("getting logger: %w", err)
	}

	switch action {
	case serviceActionInstall:
		return controlInstall(l, svc, confPath)
	case serviceActionRestart:
		return svc.Restart()
	case serviceActionStart:
		return svc.Start()
	case serviceActionStatus:
		return controlStatus(svc)
	case serviceActionStop:
		return svc.Stop()
	case serviceActionUninstall:
		return svc.Uninstall()
	default:
		panic(errUnknownAction)
	}
}

// controlInstall generates a default configuration file, if necessary, and
// installs svc as the system service.
func controlInstall(l osservice.Logger, svc osservice.Service, confPath string) (err error) {
	err = writeDefaultConfig(l, confPath)
	if err != nil {
		// Don't wrap the error since it's informative enough as is.
		return err
	}

	return svc.Install()
}

// controlStatus prints the status of the system service corresponding to svc.
// It returns an error if the appropriate exit code should be used.
func controlStatus(svc osservice.Service) (err error) {
	status, err := svc.Status()
	if err != nil {
		return fmt.Errorf("retrieving status: %w", err)
	}

	var msg string
	switch status {
	case osservice.StatusRunning:
		msg = "running"
	case osservice.StatusStopped:
		msg = "stopped"
	default:
		// Don't expect [osservice.StatusUnknown] here, since it's only returned
		// on error.
		//
		// TODO(e.burkov):  Consider panicking here.
		return fmt.Errorf("unexpected status %d", status)
	}

	_, _ = fmt.Fprintln(os.Stdout, msg)

	return nil
}

// defaultService is the implementation of the [osservice.Interface] interface
// for AdGuardDNSClient.
type defaultService struct {
	opts     *options
	done     chan struct{}
	errCh    chan error
	confPath string
}

// type check
var _ osservice.Interface = (*defaultService)(nil)

// Start implements the [osservice.Interface] interface for [*defaultService].
func (svc *defaultService) Start(ossvc osservice.Service) (err error) {
	conf, err := parseConfig(svc.confPath)
	if err != nil {
		// Don't wrap the error, since it's informative enough as is.
		return err
	}

	err = conf.validate()
	if err != nil {
		return fmt.Errorf("validating: %w", err)
	}

	isVerbose := svc.opts.verbose || conf.Log.Verbose
	l := logger(isVerbose)

	ctx := context.Background()

	// TODO(a.garipov): Copy logs configuration from the WIP abt. slog.
	l.InfoContext(
		ctx,
		"AdGuardDNSClient starting",
		"version", version.Version(),
		"revision", version.Revision(),
		"branch", version.Branch(),
		"commit_time", version.CommitTime(),
		"race", version.RaceEnabled,
		"verbose", isVerbose,
	)

	svcHdlr := newServiceHandler(svc.done, service.SignalHandlerShutdownTimeout)

	dnsSvc, err := dnssvc.New(conf.DNS.toInternal())
	if err != nil {
		return fmt.Errorf("creating dns service: %w", err)
	}

	err = dnsSvc.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting dns service: %w", err)
	}

	svcHdlr.add(dnsSvc)
	l.DebugContext(ctx, "dns service started")

	go svcHdlr.handle(ctx, l.With(slogutil.KeyPrefix, serviceHandlerPrefix), svc.errCh)

	return nil
}

// Stop implements the [osservice.Interface] interface for [*defaultService].
func (svc *defaultService) Stop(_ osservice.Service) (err error) {
	close(svc.done)

	return <-svc.errCh
}
