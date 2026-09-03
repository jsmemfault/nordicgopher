module nordicgopher

// Minimum 1.21, required by log/slog. Do not raise this without reason: the
// deployment host may carry an older toolchain than the workstation, and the
// server binary is normally cross-compiled anyway (see `make dist`).
go 1.21
