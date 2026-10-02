# Run workflows and manage schedules

Open a service's Workflows view to see declared workflow entrypoints, start runs and administer schedules. Temporal must be enabled. A service declares workflows; operators own schedule definitions.

## Start and inspect a run

1. Select the declared workflow and fill its typed input.
2. Review the workflow ID and execution options.
3. Start once, then inspect the returned run.
4. Follow status, history and result. Keep the raw payload when a schema is unavailable or the result cannot be decoded.

Cancel asks execution to stop cooperatively. Terminate ends it administratively. A signal delivers a named application message. These operations can have external effects and are audited; a deadline or lost response does not establish whether they were accepted.

## Create a schedule

Select a workflow such as hello's `Report`, enter its input and schedule specification, review the policy, then create it. Check the next execution and actual resulting run. The UI/API support editing and deleting schedules, pausing/resuming them and triggering an immediate run.

Use a meaningful pause note. An update must preserve operator pause state unless explicitly changed. Concurrent changes are protected by revision checks. When a conflict occurs, reload and compare instead of overwriting another administrator's update.

## Rolling updates

SDK startup does not reconcile schedules. Restarting a service therefore does not reset schedules or resume them. Workflow names and payload compatibility still matter: a schedule may target code that a rollout removed. Review schedules before removing a workflow declaration.

## Uncertain command outcomes

External commands persist an audit intent before dispatch and a result afterward. An intent without result or an `unknown` result requires investigation. Match the operation ID with the run/schedule state; do not assume a retry is safe because the browser showed a timeout.

See [WorkflowService](../reference/services/workflow-service.md), [ScheduleService](../reference/services/schedule-service.md) and [audit semantics](audit.md).
