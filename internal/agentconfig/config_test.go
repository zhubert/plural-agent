package agentconfig

import "github.com/zhubert/erg/internal/config"

// Compile-time interface satisfaction check.
var _ Config = (*config.Config)(nil)
