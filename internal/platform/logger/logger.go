package logger

import "go.uber.org/zap"

// New builds a production-mode zap logger (JSON, structured fields) at the
// given level ("debug", "info", "warn", "error"). Never use fmt.Printf
// anywhere else in this codebase — always log through the *zap.Logger
// returned here.
func New(level string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	var lvl zap.AtomicLevel
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = zap.NewAtomicLevelAt(zap.InfoLevel)
	}
	cfg.Level = lvl
	return cfg.Build()
}
