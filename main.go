// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/profiler"
	"github.com/sirupsen/logrus"

	"google.golang.org/grpc"
	// "google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
	// "google.golang.org/grpc/status"

	// OpenTelemetry (OTLP gRPC → New Relic)
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/runtime"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"

	pb "github.com/GoogleCloudPlatform/microservices-demo/src/shippingservice/genproto"
	health "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	defaultPort = "50051"
)

var log *logrus.Logger

func init() {
	log = logrus.New()
	log.Level = logrus.DebugLevel
	log.Formatter = &logrus.JSONFormatter{
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "severity",
			logrus.FieldKeyMsg:   "message",
		},
		TimestampFormat: time.RFC3339Nano,
	}
	log.Out = os.Stdout
}

func main() {
	// legacy toggles kept (not used for OTel)
	if os.Getenv("DISABLE_TRACING") == "" {
		log.Info("Tracing enabled, but temporarily unavailable")
		go initTracing()
	} else {
		log.Info("Tracing disabled.")
	}
	if os.Getenv("DISABLE_PROFILER") == "" {
		log.Info("Profiling enabled.")
		go initProfiling("shippingservice", "1.0.0")
	} else {
		log.Info("Profiling disabled.")
	}

	// ---------- OpenTelemetry (OTLP gRPC) ----------
	ctx := context.Background()
	var shutdown func(context.Context) error
	if os.Getenv("DISABLE_OTEL") == "" {
		sd, err := initTelemetry(ctx)
		if err != nil {
			log.Fatalf("otel init failed: %v", err)
		}
		shutdown = sd
		log.Info("OpenTelemetry (OTLP/gRPC) initialized")
	} else {
		log.Info("OpenTelemetry disabled via env")
	}
	defer func() {
		if shutdown != nil {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(c)
		}
	}()

	// ---------- gRPC server ----------
	port := defaultPort
	if value, ok := os.LookupEnv("PORT"); ok {
		port = value
	}
	addr := fmt.Sprintf(":%s", port)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	// Instrument gRPC server (traces + metrics)
	otelHandler := otelgrpc.NewServerHandler(
		otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents, otelgrpc.SentEvents),
	)
	srv := grpc.NewServer(
		grpc.StatsHandler(otelHandler),
	)

	// app service
	svc := &server{}
	pb.RegisterShippingServiceServer(srv, svc)

	// built-in health server (satisfies latest Health API incl. List)
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, hs)

	log.Infof("Shipping Service listening on %s", addr)

	// reflection for debugging
	reflection.Register(srv)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

// server controls RPC service responses.
type server struct {
	pb.UnimplementedShippingServiceServer
}

// GetQuote produces a shipping quote (cost) in USD.
func (s *server) GetQuote(ctx context.Context, in *pb.GetQuoteRequest) (*pb.GetQuoteResponse, error) {
	log.Info("[GetQuote] received request")
	defer log.Info("[GetQuote] completed request")

	// Example: compute quote (keep your real logic)
	quote := CreateQuoteFromCount(0)

	return &pb.GetQuoteResponse{
		CostUsd: &pb.Money{
			CurrencyCode: "USD",
			Units:        int64(quote.Dollars),
			Nanos:        int32(quote.Cents * 10000000),
		},
	}, nil
}

// ShipOrder mocks that the requested items will be shipped.
func (s *server) ShipOrder(ctx context.Context, in *pb.ShipOrderRequest) (*pb.ShipOrderResponse, error) {
	log.Info("[ShipOrder] received request")
	defer log.Info("[ShipOrder] completed request")

	baseAddress := fmt.Sprintf("%s, %s, %s", in.Address.StreetAddress, in.Address.City, in.Address.State)
	id := CreateTrackingId(baseAddress)

	return &pb.ShipOrderResponse{TrackingId: id}, nil
}

func initStats() {
	// OpenTelemetry metrics are initialized in initTelemetry
}

func initTracing() {
	// Legacy placeholder; actual tracing via OpenTelemetry in initTelemetry
}

func initProfiling(service, version string) {
	for i := 1; i <= 3; i++ {
		if err := profiler.Start(profiler.Config{
			Service:        service,
			ServiceVersion: version,
		}); err != nil {
			log.Warnf("failed to start profiler: %+v", err)
		} else {
			log.Info("started Stackdriver profiler")
			return
		}
		d := time.Second * 10 * time.Duration(i)
		log.Infof("sleeping %v to retry initializing Stackdriver profiler", d)
		time.Sleep(d)
	}
	log.Warn("could not initialize Stackdriver profiler after retrying, giving up")
}

// ---------------- OTel wiring (OTLP gRPC → New Relic) ----------------

func initTelemetry(ctx context.Context) (func(context.Context) error, error) {
	// Endpoint & headers for New Relic
	// US: otlp.nr-data.net:4317  |  EU: otlp.eu01.nr-data.net:4317
	endpoint := getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otlp.nr-data.net:4317")
	headers := getenv("OTEL_EXPORTER_OTLP_HEADERS", "") // must include: api-key=<NEW_RELIC_LICENSE_KEY>

	serviceName := getenv("OTEL_SERVICE_NAME", "shippingservice")
	serviceVersion := getenv("OTEL_SERVICE_VERSION", "1.0.0")
	deployEnv := getAttr("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment", "prod")

	// Resource (no semconv needed)
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", serviceVersion),
			attribute.String("deployment.environment", deployEnv),
		),
	)
	if err != nil {
		return nil, err
	}

	// Exporters (TLS + gzip)
	creds := credentials.NewTLS(&tls.Config{})
	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithHeaders(parseHeaders(headers)),
		otlptracegrpc.WithTLSCredentials(creds),
		otlptracegrpc.WithCompressor("gzip"),
	)
	if err != nil {
		return nil, err
	}

	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithHeaders(parseHeaders(headers)),
		otlpmetricgrpc.WithTLSCredentials(creds),
		otlpmetricgrpc.WithCompressor("gzip"),
	)
	if err != nil {
		return nil, err
	}

	// Providers
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// Useful runtime metrics (GC, goroutines, memory)
	_ = runtime.Start(runtime.WithMinimumReadMemStatsInterval(10*time.Second))

	// Shutdown func to flush/close exporters
	return func(ctx context.Context) error {
		if err := mp.Shutdown(ctx); err != nil {
			log.Warnf("metrics shutdown: %v", err)
		}
		if err := tp.Shutdown(ctx); err != nil {
			log.Warnf("traces shutdown: %v", err)
		}
		return nil
	}, nil
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func getAttr(envKey, key, defVal string) string {
	raw := os.Getenv(envKey)
	if strings.TrimSpace(raw) == "" {
		return defVal
	}
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == key {
			return kv[1]
		}
	}
	return defVal
}

func parseHeaders(h string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(h) == "" {
		return out
	}
	for _, pair := range strings.Split(h, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) == 2 {
			out[kv[0]] = kv[1]
		}
	}
	return out
}
