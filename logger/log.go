package logger

import (
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	Logger      *zap.Logger
	atomicLevel zap.AtomicLevel
)

const (
	_ = iota
	DEBUG
	INFO
	WARNING
	ERROR
	FATAL
)

// init zap logger
func LogInit() *zap.Logger {

	encoderConfig := zapcore.EncoderConfig{
		LevelKey:       "level",
		TimeKey:        "time",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    customLevelEncoder,
		EncodeTime:     customTimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}

	encoder := zapcore.NewConsoleEncoder(encoderConfig)

	atomicLevel = zap.NewAtomicLevel()
	atomicLevel.SetLevel(zap.InfoLevel)

	core := zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), atomicLevel)
	return zap.New(core, zap.AddCaller())

}

func customLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + level.String() + "]")
}

func customTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02T15:04:05.000"))
}

func SetLogLevel(level int) {
	var zapLevel zapcore.Level
	switch level {
	case DEBUG:
		zapLevel = zap.DebugLevel
	case INFO:
		zapLevel = zap.InfoLevel
	case WARNING:
		zapLevel = zap.WarnLevel
	case ERROR:
		zapLevel = zap.ErrorLevel
	case FATAL:
		zapLevel = zap.FatalLevel
	}
	atomicLevel.SetLevel(zapLevel)
}
