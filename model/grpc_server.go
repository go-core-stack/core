// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package model

import (
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

type GrpcServerContext struct {
	// GRPC server handle, over which the grpc server is hosted,
	// This handle will be used by the service providers to plumb
	// newly created GRPC server handlers
	Server *grpc.Server

	// GRPC gateway mux handle, used to register the GRPC gateway
	// http handle as per the grpc gateway spec, with under neath
	// Endpoint registered catching http requests on the server
	Mux *runtime.ServeMux

	// GRPC client handle typically to the above grpc server itself
	// required by GRPC gateway to plumb between server and mux
	Conn *grpc.ClientConn
}
