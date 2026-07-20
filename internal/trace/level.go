// Package trace defines a custom slog level for detailed investigation pipeline logging.
package trace

import "log/slog"

// LevelTrace is a custom slog level below Debug (-4), used for detailed
// investigation pipeline logging. Enabled via TRACE=true.
const LevelTrace = slog.Level(-8)
