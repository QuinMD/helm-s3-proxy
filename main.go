package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/QuinMD/helm-s3-proxy/internal/handler"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/aws/aws-sdk-go/service/sts"

	"github.com/cresta/gotracing"
	"github.com/cresta/gotracing/datadog"
	"github.com/cresta/httpsimple"
	"github.com/cresta/zapctx"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type config struct {
	ListenAddr      string
	DebugListenAddr string
	Tracer          string
	LogLevel        string
	S3Bucket        string
	ReplaceHTTPPath string
}

func (c config) WithDefaults() config {
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.DebugListenAddr == "" {
		c.DebugListenAddr = ":6060"
	}
	if c.LogLevel == "" {
		c.LogLevel = "INFO"
	}
	return c
}

func getConfig() config {
	return config{
		// Defaults to ":8080"
		ListenAddr: os.Getenv("LISTEN_ADDR"),
		// Defaults to ":6060"
		DebugListenAddr: os.Getenv("DEBUG_ADDR"),
		// Allows you to use a dynamic tracer
		Tracer: os.Getenv("TRACER"),
		// Level to log at
		LogLevel:        os.Getenv("LOG_LEVEL"),
		S3Bucket:        os.Getenv("S3_BUCKET"),
		ReplaceHTTPPath: os.Getenv("REPLACE_HTTP_PATH"),
	}.WithDefaults()
}

func main() {
	instance.Main()
}

type Service struct {
	osExit   func(int)
	config   config
	log      *zapctx.Logger
	onListen func(net.Listener)
	server   *http.Server
	tracers  *gotracing.Registry
}

var instance = Service{
	osExit: os.Exit,
	config: getConfig(),
	tracers: &gotracing.Registry{
		Constructors: map[string]gotracing.Constructor{
			"datadog": datadog.NewTracer,
		},
	},
}

func setupLogging(logLevel string) (*zapctx.Logger, error) {
	zapCfg := zap.NewProductionConfig()
	var lvl zapcore.Level
	logLevelErr := lvl.UnmarshalText([]byte(logLevel))
	if logLevelErr == nil {
		zapCfg.Level.SetLevel(lvl)
	}
	l, err := zapCfg.Build(zap.AddCaller())
	if err != nil {
		return nil, err
	}
	retLogger := zapctx.New(l)
	retLogger.IfErr(logLevelErr).Warn(context.Background(), "unable to parse log level")
	return retLogger, nil
}

func (m *Service) Main() {
	cfg := m.config
	if m.log == nil {
		var err error
		m.log, err = setupLogging(m.config.LogLevel)
		if err != nil {
			fmt.Printf("Unable to setup logging: %v", err)
			m.osExit(1)
			return
		}
	}
	m.log.Info(context.Background(), "Starting", zap.Any("config", m.config))
	rootTracer, err := m.tracers.New(m.config.Tracer, gotracing.Config{
		Log: m.log.With(zap.String("section", "setup_tracing")),
		Env: os.Environ(),
	})
	if err != nil {
		m.log.IfErr(err).Error(context.Background(), "unable to setup tracing")
		m.osExit(1)
		return
	}

	ctx := context.Background()
	m.log = m.log.DynamicFields(rootTracer.DynamicFields()...)

	m.server, err = m.setupServer(cfg, m.log, rootTracer)
	if err != nil {
		m.log.IfErr(err).Panic(context.Background(), "unable to setup HTTP server")
		m.osExit(1)
		return
	}
	shutdownCallback, err := setupDebugServer(m.log, cfg.DebugListenAddr)
	if err != nil {
		m.log.IfErr(err).Panic(context.Background(), "unable to setup debug server")
		m.osExit(1)
		return
	}
	m.log.Info(ctx, "Listening on HTTP", zap.String("addr", m.config.ListenAddr))
	serveErr := httpsimple.BasicServerRun(m.log, m.server, m.onListen, m.config.ListenAddr)
	if err := shutdownCallback(ctx); err != nil {
		m.log.IfErr(err).Error(ctx, "unable to shutdown debug server")
	}
	if serveErr != nil {
		m.osExit(1)
	}
}

func (m *Service) setupServer(cfg config, log *zapctx.Logger, tracer gotracing.Tracing) (*http.Server, error) {
	rootHandler := mux.NewRouter()
	rootHandler.Handle("/health", httpsimple.HealthHandler(log, tracer))
	sess, err := session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create aws session: %w", err)
	}
	stsClient := sts.New(sess)
	resp, err := stsClient.GetCallerIdentityWithContext(context.Background(), &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("unable to get caller identity: %w", err)
	}
	log.Info(context.Background(), "Got identity", zap.String("identity", *resp.Account))

	bh := handler.BucketHandler{
		Bucket:          m.config.S3Bucket,
		ReplaceHTTPPath: m.config.ReplaceHTTPPath,
		Downloader:      s3manager.NewDownloader(sess),
		Log:             m.log.With(zap.String("section", "bucket_handler")),
	}
	if err := bh.Setup(rootHandler); err != nil {
		return nil, fmt.Errorf("unable to setup bucket handler: %w", err)
	}
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 2 * time.Second,
	}, nil
}

func setupDebugServer(l *zapctx.Logger, listenAddr string) (func(ctx context.Context) error, error) {
	if listenAddr == "" || listenAddr == "-" {
		return func(_ context.Context) error { return nil }, nil
	}
	s := httpsimple.DebugServer{
		Logger:     l,
		ListenAddr: listenAddr,
	}
	if err := s.Setup(); err != nil {
		return nil, err
	}
	go func() {
		if err := s.Start(); err != nil {
			l.IfErr(err).Warn(context.Background(), "debug server crashed")
		}
	}()
	return s.Shutdown, nil
}
