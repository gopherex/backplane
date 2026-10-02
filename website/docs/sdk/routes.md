# Public routes and internal APIs

The SDK can own HTTP/gRPC listeners or announce routes served by another listener. Route declarations are included in the manifest and can become Envoy configuration.

## Managed endpoints

The hello service registers one protobuf implementation for gRPC, Connect, REST transcoding and ws-proto:

```go
svc.GRPC(st.Greeter.Register,
    route.Transcode(), route.Reflection(),
    route.Timeout(time.Minute))

wsproto.Serve(svc, "/ws/", route.Origins{}, st.Greeter.Register)

svc.HTTP("/hello/", handler,
    route.OpenAPIFS(apiFiles, "openapi.yaml"),
    route.Timeout(5*time.Second))
```

`Register` accepts a `grpc.ServiceRegistrar`. A zero ws-proto origin policy permits same-origin browsers and non-browser clients. Configure cross-origin access deliberately; wildcard CORS is not authentication.

Route options include middleware/interceptors, timeouts, request bounds and CORS policy. Choose stream timeouts that fit streaming methods rather than copying a short unary timeout. Native handler behavior and gateway policy must both suit the protocol.

## Existing listeners

If your application owns a listener, announce it with `svc.Route`:

```go
svc.Route(route.HTTP("/legacy/",
    route.Port(8083),
    route.Timeout(5*time.Second),
    route.OpenAPI(document)))
```

This does not create the listener or handler. Your lifecycle must run it, and the advertised address/port must be reachable. The hello legacy listener is a working example with its own API document; port 8083 is not a second console.

## Internal console RPCs

Register administrative RPCs with `svc.Internal`. A module generates TypeScript clients from those protobuf definitions and calls them through the host's shared client. The authenticated console relay reaches the service's platform listener.

Keep public and administrative methods separate. Internal listeners need installation-secret protection and network isolation as described in [security](../deploying/security.md). Publishing a UI bundle is not authorization for an otherwise public application endpoint.

## API documentation

HTTP routes accept OpenAPI, gRPC/Connect/ws-proto use descriptor sets, and GraphQL uses introspection JSON. Multi-file OpenAPI documents can be embedded as a filesystem. [Publishing service API documents](service-api.md) covers packaging, limits and reference resolution.
