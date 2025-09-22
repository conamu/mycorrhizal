package mycorrizal

import "log/slog"

type Mycorrizal interface {
	Start() error
}

type mycorrizal struct{
	l *slog.Logger
}

func New(cfg *Config) (Mycorrizal, error) {
	return &mycorrizal{
		l: cfg.logger,
	}, nil
}

func (mc *mycorrizal) Start() error {
	mc.l.Info("mycorrizal started")
	return nil
}
