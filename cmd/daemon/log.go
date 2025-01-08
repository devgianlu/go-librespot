package main

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/sirupsen/logrus"
)

type LogrusAdapter struct {
	Log *logrus.Entry
}

func (l LogrusAdapter) Tracef(format string, args ...interface{}) {
	l.Log.Tracef(format, args...)
}

func (l LogrusAdapter) Debugf(format string, args ...interface{}) {
	l.Log.Debugf(format, args...)
}

func (l LogrusAdapter) Infof(format string, args ...interface{}) {
	l.Log.Infof(format, args...)
}

func (l LogrusAdapter) Warnf(format string, args ...interface{}) {
	l.Log.Warnf(format, args...)
}

func (l LogrusAdapter) Errorf(format string, args ...interface{}) {
	l.Log.Errorf(format, args...)
}

func (l LogrusAdapter) Trace(args ...interface{}) {
	l.Log.Trace(args...)
}

func (l LogrusAdapter) Debug(args ...interface{}) {
	l.Log.Debug(args...)
}

func (l LogrusAdapter) Info(args ...interface{}) {
	l.Log.Info(args...)
}

func (l LogrusAdapter) Warn(args ...interface{}) {
	l.Log.Warn(args...)
}

func (l LogrusAdapter) Error(args ...interface{}) {
	l.Log.Error(args...)
}

func (l LogrusAdapter) WithField(key string, value interface{}) librespot.Logger {
	return LogrusAdapter{l.Log.WithField(key, value)}
}

func (l LogrusAdapter) WithError(err error) librespot.Logger {
	return LogrusAdapter{l.Log.WithError(err)}
}
