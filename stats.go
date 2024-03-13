package nebula

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	graphite "github.com/cyberdelia/go-metrics-graphite"
	mp "github.com/nbrownus/go-metrics-prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rcrowley/go-metrics"
	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula/config"
)

// startStats initializes stats from config. On success, if any further work
// is needed to serve stats, it returns a func to handle that work. If no
// work is needed, it'll return nil. On failure, it returns nil, error.
func startStats(l *logrus.Logger, c *config.C, buildVersion string, configTest bool) (func(), error) {
	mType := c.GetString("stats.type").UnwrapOrDefault()
	if mType == "" || mType == "none" {
		return nil, nil
	}

	interval := c.GetDuration("stats.interval").UnwrapOr(0)
	if interval == 0 {
		return nil, fmt.Errorf("stats.interval was an invalid duration: %s", c.GetString("stats.interval").UnwrapOrDefault())
	}

	var startFn func()
	switch mType {
	case "graphite":
		err := startGraphiteStats(l, interval, c, configTest)
		if err != nil {
			return nil, err
		}
	case "prometheus":
		var err error
		startFn, err = startPrometheusStats(l, interval, c, buildVersion, configTest)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("stats.type was not understood: %s", mType)
	}

	metrics.RegisterDebugGCStats(metrics.DefaultRegistry)
	metrics.RegisterRuntimeMemStats(metrics.DefaultRegistry)

	go metrics.CaptureDebugGCStats(metrics.DefaultRegistry, interval)
	go metrics.CaptureRuntimeMemStats(metrics.DefaultRegistry, interval)

	return startFn, nil
}

func startGraphiteStats(l *logrus.Logger, i time.Duration, c *config.C, configTest bool) error {
	proto := c.GetString("stats.protocol").UnwrapOr("tcp")
	host := c.GetString("stats.host").UnwrapOrDefault()
	if host == "" {
		return errors.New("stats.host can not be empty")
	}

	prefix := c.GetString("stats.prefix").UnwrapOr("nebula")
	addr, err := net.ResolveTCPAddr(proto, host)
	if err != nil {
		return fmt.Errorf("error while setting up graphite sink: %s", err)
	}

	if !configTest {
		l.Infof("Starting graphite. Interval: %s, prefix: %s, addr: %s", i, prefix, addr)
		go graphite.Graphite(metrics.DefaultRegistry, i, prefix, addr)
	}
	return nil
}

func startPrometheusStats(l *logrus.Logger, i time.Duration, c *config.C, buildVersion string, configTest bool) (func(), error) {
	namespace := c.GetString("stats.namespace").UnwrapOrDefault()
	subsystem := c.GetString("stats.subsystem").UnwrapOrDefault()

	listen := c.GetString("stats.listen").UnwrapOrDefault()
	if listen == "" {
		return nil, fmt.Errorf("stats.listen should not be empty")
	}

	path := c.GetString("stats.path").UnwrapOrDefault()
	if path == "" {
		return nil, fmt.Errorf("stats.path should not be empty")
	}

	pr := prometheus.NewRegistry()
	pClient := mp.NewPrometheusProvider(metrics.DefaultRegistry, namespace, subsystem, pr, i)
	if !configTest {
		go pClient.UpdatePrometheusMetrics()
	}

	// Export our version information as labels on a static gauge
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "info",
		Help:      "Version information for the Nebula binary",
		ConstLabels: prometheus.Labels{
			"version":      buildVersion,
			"goversion":    runtime.Version(),
			"boringcrypto": strconv.FormatBool(boringEnabled()),
		},
	})
	pr.MustRegister(g)
	g.Set(1)

	var startFn func()
	if !configTest {
		startFn = func() {
			l.Infof("Prometheus stats listening on %s at %s", listen, path)
			http.Handle(path, promhttp.HandlerFor(pr, promhttp.HandlerOpts{ErrorLog: l}))
			log.Fatal(http.ListenAndServe(listen, nil))
		}
	}

	return startFn, nil
}
