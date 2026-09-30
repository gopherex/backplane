package backplane

import "github.com/gopherex/xlog"

// Application audit (§14): a log record carrying AuditLabel=true is audit.
// The deployment's Collector forwards such records to backplane, which keeps
// them; the log itself goes wherever logs go. The rest of the keys are
// optional and filterable in the console's Audit; the action is the record's
// event.name, else its message.
const (
	AuditLabel = "backplane.audit"
	// AuditID deduplicates a record the Collector retries; without it the
	// record's content does.
	AuditID      = "backplane.audit.id"
	AuditActor   = "backplane.audit.actor"
	AuditSubject = "backplane.audit.subject"
	// AuditOutcome: succeeded, failed or rejected by convention.
	AuditOutcome = "backplane.audit.outcome"
)

// Audit marks a log record as application audit:
//
//	log.Info("identity deleted", backplane.Audit(),
//		xlog.String("event.name", "identity.deleted"),
//		xlog.String(backplane.AuditSubject, "identity/"+id))
func Audit() xlog.Field { return xlog.Bool(AuditLabel, true) }
