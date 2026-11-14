package main

import (
	"context"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// rpcLogInterceptor writes ONE log line per unary RPC, after the handler returned: message "rpc", fields method (the
// full RPC method), code (gRPC status code string) and duration_ms. It logs with l.WithContext(ctx), so the line carries
// trace_id/span_id of the server span (the otelgrpc stats handler starts it before the interceptors run) and the
// request_id that obs.UnaryServerInterceptor put into the context: put it AFTER that interceptor in the chain.
//
// Level: info for OK and for client-side codes (NotFound, InvalidArgument, ...); error, with the error text, for
// Internal, Unavailable, Unknown and DataLoss. The gRPC health and reflection methods are not logged (the container
// HEALTHCHECK and the probes call health every few seconds). Request and response messages are never logged.
func rpcLogInterceptor(l *logrus.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		start := time.Now() // monotonic reading: not affected by wall clock jumps
		resp, err := h(ctx, req)
		if silentRPC(info.FullMethod) {
			return resp, err
		}
		code := status.Code(err)
		e := l.WithContext(ctx).WithFields(logrus.Fields{
			"method":      info.FullMethod,
			"code":        code.String(),
			"duration_ms": float64(time.Since(start).Microseconds()) / 1000,
		})
		switch code {
		case codes.Internal, codes.Unavailable, codes.Unknown, codes.DataLoss:
			e.WithError(err).Error("rpc")
		default:
			e.Info("rpc")
		}
		return resp, err
	}
}

// silentRPC reports the methods that are not logged: health checks and server reflection.
func silentRPC(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.v1.Health/") || strings.HasPrefix(fullMethod, "/grpc.reflection.")
}
