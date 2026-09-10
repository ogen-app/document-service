// Package config loads document-service settings from the environment via
// envconfig.
package config

import (
	"fmt"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

// Config holds the service settings. Read from the environment with no global
// prefix (each field names its own var), matching the Ogen API's config style.
type Config struct {
	// Listen is the gRPC listen address. Accepts a full "host:port" or a bare
	// port (e.g. "50051", as Railway's PORT provides) — Load normalises a bare
	// port to ":port" so net.Listen gets the leading colon it requires.
	Listen string `envconfig:"DOCUMENTS_SERVICE_LISTEN" default:":50051"`
	// MaxConcurrent bounds how many documents are extracted at once. Extraction is
	// CPU-bound (inflate + XML scan), so this guards a burst of large uploads from
	// saturating the pod. <=0 resolves to GOMAXPROCS at boot.
	MaxConcurrent int `envconfig:"DOCUMENTS_SERVICE_MAX_CONCURRENT" default:"0"`
	// GCPercent sets the GC target (Go's GOGC) via debug.SetGCPercent at boot. A
	// lower value collects more often, trading CPU for a smaller heap high-water
	// mark. <=0 leaves the runtime default (100). Unlike pdf-service there is no
	// CGO, so this governs the entire working set.
	GCPercent int `envconfig:"DOCUMENTS_SERVICE_GC_PERCENT" default:"50"`
	// MemoryLimitRatio sets Go's soft memory limit (GOMEMLIMIT) to this fraction of
	// the container's cgroup memory limit, read at boot, so the GC leans harder as
	// the heap nears the cap and RSS stays bounded under a burst. Ignored when
	// GOMEMLIMIT is set explicitly, when <=0, or when no finite cgroup limit is
	// found (e.g. local dev).
	MemoryLimitRatio float64 `envconfig:"DOCUMENTS_SERVICE_MEMORY_LIMIT_RATIO" default:"0.9"`
	// LogLevel is the minimum slog level: debug|info|warn|error. Unknown/empty
	// falls back to info. Bare LOG_LEVEL (not prefixed) matches the Ogen API's knob
	// so operators use identical settings across services (CON-107).
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`
	// LogFormat selects the slog handler: json (default, prod) or text (local).
	LogFormat string `envconfig:"LOG_FORMAT" default:"json"`
}

// Load reads and validates the configuration from the environment.
func Load() (*Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return nil, err
	}
	// A bare port like "50051" is a valid env value (Railway's PORT) but net.Listen
	// needs "host:port" — without a colon it errors "missing port in address".
	// Prefix it so the service binds all interfaces on that port.
	if c.Listen != "" && !strings.Contains(c.Listen, ":") {
		c.Listen = ":" + c.Listen
	}
	// MemoryLimitRatio is a fraction of the cgroup limit; <=0 disables the derived
	// GOMEMLIMIT (documented). A value > 1 would set GOMEMLIMIT *above* the hard
	// cgroup limit, so the GC never leans in before an OOM kill — reject it.
	if c.MemoryLimitRatio > 1 {
		return nil, fmt.Errorf("DOCUMENTS_SERVICE_MEMORY_LIMIT_RATIO must be <= 1, got %v", c.MemoryLimitRatio)
	}
	return &c, nil
}
