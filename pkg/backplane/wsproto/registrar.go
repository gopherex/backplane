package wsproto

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/proto" // registers the proto codec
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/metadata"

	"github.com/gopherex/ws-proto/wsrpc"
)

// registrar serves any protoc-gen-go-grpc service on a wsrpc.Server by
// driving the generated grpc.ServiceDesc handlers over wsrpc streams. The
// gRPC proto codec does the (de)serialization, so no per-service bridge is
// generated and no message types are asserted here.
type registrar struct {
	srv      *wsrpc.Server
	codec    encoding.CodecV2
	services []string
}

func newRegistrar(srv *wsrpc.Server) *registrar {
	return &registrar{srv: srv, codec: encoding.GetCodecV2("proto")}
}

// RegisterService implements grpc.ServiceRegistrar.
func (r *registrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	r.services = append(r.services, desc.ServiceName)
	for _, m := range desc.Methods {
		r.srv.Register("/"+desc.ServiceName+"/"+m.MethodName, r.unary("/"+desc.ServiceName+"/"+m.MethodName, m.Handler, impl))
	}

	for _, st := range desc.Streams {
		r.srv.Register("/"+desc.ServiceName+"/"+st.StreamName, r.stream(st.Handler, impl))
	}
}

// incoming turns OPEN headers into gRPC incoming metadata.
func (r *registrar) incoming(ctx context.Context, s *wsrpc.Stream) context.Context {
	md := metadata.MD{}
	for k, v := range s.Header() {
		md.Append(k, v)
	}

	return metadata.NewIncomingContext(ctx, md)
}

func (r *registrar) unary(method string, handler grpc.MethodHandler, impl any) wsrpc.Handler {
	return func(ctx context.Context, s *wsrpc.Stream) error {
		raw, err := s.RecvRaw()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		ctx, sink := wsrpc.WithUnaryMetadata(r.incoming(ctx, s))
		ctx = grpc.NewContextWithServerTransportStream(ctx, wsrpc.UnaryServerTransportStream(ctx, method))
		decode := func(v any) error { return r.codec.Unmarshal(mem.BufferSlice{mem.SliceBuffer(raw)}, v) }

		res, err := handler(impl, ctx, decode, nil)
		if err != nil {
			return err
		}

		out, err := r.codec.Marshal(res)
		if err != nil {
			return err
		}
		defer out.Free()

		if h := sink.Header(); h != nil {
			_ = s.SendHeader(h)
		}

		s.SetTrailer(sink.Trailer())

		return s.SendRaw(out.Materialize())
	}
}

func (r *registrar) stream(handler grpc.StreamHandler, impl any) wsrpc.Handler {
	return func(ctx context.Context, s *wsrpc.Stream) error {
		return handler(impl, &serverStream{s: s, ctx: r.incoming(ctx, s), codec: r.codec})
	}
}

// serverStream adapts *wsrpc.Stream to grpc.ServerStream.
type serverStream struct {
	s       *wsrpc.Stream
	ctx     context.Context //nolint:containedctx // a ServerStream carries its context by contract
	codec   encoding.CodecV2
	pending metadata.MD
}

func (x *serverStream) Context() context.Context { return x.ctx }

func (x *serverStream) SetHeader(md metadata.MD) error {
	x.pending = metadata.Join(x.pending, md)
	return nil
}

func (x *serverStream) SendHeader(md metadata.MD) error {
	x.pending = metadata.Join(x.pending, md)
	err := x.s.SendHeader(wsrpc.FlattenMD(x.pending))
	x.pending = nil

	return err
}

func (x *serverStream) SetTrailer(md metadata.MD) { x.s.SetTrailer(wsrpc.FlattenMD(md)) }

func (x *serverStream) SendMsg(m any) error {
	if x.pending != nil {
		_ = x.s.SendHeader(wsrpc.FlattenMD(x.pending))
		x.pending = nil
	}

	out, err := x.codec.Marshal(m)
	if err != nil {
		return err
	}
	defer out.Free()

	return x.s.SendRaw(out.Materialize())
}

func (x *serverStream) RecvMsg(m any) error {
	raw, err := x.s.RecvRaw()
	if err != nil {
		return err
	}

	return x.codec.Unmarshal(mem.BufferSlice{mem.SliceBuffer(raw)}, m)
}
