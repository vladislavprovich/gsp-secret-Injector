package signal

import (
	"io/ioutil"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
)

func ForwardToPid(pid int, logger *logrus.Logger, signals ...os.Signal) {
	log := nullLogger()
	if logger != nil {
		log = logger
	}

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, signals...)

	go func() {
		signalStream := <-signalChannel
		(*log).Infof("signal received: %v\n", signalStream)
		if err := syscall.Kill(-pid, signalStream.(syscall.Signal)); err != nil {
			(*log).WithError(err).Error("failed to forward signal")
		}
	}()
}

func nullLogger() *logrus.Logger {
	logger := logrus.New()
	logger.Out = ioutil.Discard

	return logger
}
