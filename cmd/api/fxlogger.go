package main

import (
	"log/slog"

	"go.uber.org/fx/fxevent"
)

type fxSlogLogger struct {
	logger *slog.Logger
}

func (l *fxSlogLogger) LogEvent(event fxevent.Event) {
	switch e := event.(type) {
	case *fxevent.OnStartExecuting:
		l.logger.Debug("fx hook starting", slog.String("callee", e.FunctionName))
	case *fxevent.OnStartExecuted:
		if e.Err != nil {
			l.logger.Error("fx hook failed",
				slog.String("callee", e.FunctionName), slog.String("error", e.Err.Error()))
			return
		}
		l.logger.Debug("fx hook started",
			slog.String("callee", e.FunctionName), slog.Duration("runtime", e.Runtime))
	case *fxevent.OnStopExecuted:
		if e.Err != nil {
			l.logger.Error("fx stop hook failed",
				slog.String("callee", e.FunctionName), slog.String("error", e.Err.Error()))
			return
		}
		l.logger.Debug("fx hook stopped", slog.String("callee", e.FunctionName))
	case *fxevent.Provided:
		if e.Err != nil {
			l.logger.Error("fx provide failed", slog.String("error", e.Err.Error()))
		}
	case *fxevent.Invoked:
		if e.Err != nil {
			l.logger.Error("fx invoke failed",
				slog.String("function", e.FunctionName), slog.String("error", e.Err.Error()))
		}
	case *fxevent.Started:
		if e.Err != nil {
			l.logger.Error("fx application failed to start", slog.String("error", e.Err.Error()))
			return
		}
		l.logger.Info("application started")
	case *fxevent.Stopped:
		if e.Err != nil {
			l.logger.Error("fx application stop failed", slog.String("error", e.Err.Error()))
			return
		}
		l.logger.Info("application stopped")
	}
}
