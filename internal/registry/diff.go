package registry

import (
	"github.com/gopherex/xlog"
)

// logDiff logs what changed between two snapshots at the level of
// services, manifests and instances (appeared, gone, registration and
// health); state updates of a known instance are not logged.
func logDiff(log *xlog.Logger, prev, next Catalog) {
	for name, svc := range next.Services {
		old, known := prev.Services[name]
		if !known {
			log.Info("service appeared", xlog.String("svc", name),
				xlog.Int("manifests", len(svc.Manifests)), xlog.Int("instances", len(svc.Instances)))
		}

		for v := range svc.Manifests {
			if _, ok := old.Manifests[v]; !ok {
				log.Info("manifest appeared", xlog.String("svc", name), xlog.String("svc_version", v))
			}
		}

		for v := range old.Manifests {
			if _, ok := svc.Manifests[v]; !ok {
				log.Info("manifest gone", xlog.String("svc", name), xlog.String("svc_version", v))
			}
		}

		logInstances(log, name, old, svc)
	}

	for name := range prev.Services {
		if _, ok := next.Services[name]; !ok {
			log.Info("service gone", xlog.String("svc", name))
		}
	}
}

func logInstances(log *xlog.Logger, name string, old, svc Service) {
	was := make(map[string]Instance, len(old.Instances))
	for _, in := range old.Instances {
		was[in.ID] = in
	}

	for _, in := range svc.Instances {
		attrs := []xlog.Field{
			xlog.String("svc", name), xlog.String("svc_instance", in.ID),
			xlog.Bool("registered", in.Registered), xlog.Bool("healthy", in.Healthy),
			xlog.String("svc_version", in.State.GetVersion()), xlog.String("phase", in.State.GetPhase().String()),
		}

		o, ok := was[in.ID]

		switch {
		case !ok:
			log.Info("instance appeared", attrs...)
		case o.Registered != in.Registered || o.Healthy != in.Healthy || (o.State == nil) != (in.State == nil) ||
			o.State.GetPhase() != in.State.GetPhase():
			log.Info("instance changed", attrs...)
		}

		delete(was, in.ID)
	}

	for id := range was {
		log.Info("instance gone", xlog.String("svc", name), xlog.String("svc_instance", id))
	}
}
