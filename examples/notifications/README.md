# Subscribe to notifications

[Source](main.go)

The notifications example opens a session with a notification handler,
subscribes with `rpc.CreateSubscription`, and logs the name and
`eventTime` of each notification. It writes no configuration and sends
no other operation.

The device must advertise `:notification:1.0` and let the account
subscribe. On Junos, configure `system services netconf notification`
first. Follow the [common setup](../README.md#common-setup), then run:

```bash
go run ./examples/notifications -host router1.example.net -user automation
```

Until Ctrl-C, SIGTERM or session end, the program logs one line per
notification to stderr:

```text
2026/10/10 13:52:02 event=syslog time=2026-10-10 13:52:01 +0200 CEST
2026/10/10 13:52:02 event=state-system-syslog time=2026-10-10 13:52:01 +0200 CEST
```

The handler does not log content, which can contain secrets. No
output means the device has sent nothing. Junos 26.2 on vJunos-router
sends every system log message twice, as above. Junos OS Evolved 26.2
sends changes of state instead. See
[Notifications](../../docs/sessions.md#notifications).

## How the program ends

When the session ends, the program fails with an error that begins with
`subscription ended; events may have been lost:` and wraps the cause, such
as `netconf.ErrNotificationOverflow` or `ncssh.ErrKeepalive`. It does not
reconnect, replay or drop duplicates. See
[Notifications and reconnects](../../docs/production-use.md#notifications-and-reconnects).

After Ctrl-C or SIGTERM, Junos 26.2 and Junos OS Evolved 26.2 do not answer
the runner's `<close-session>` unless
`system services netconf notification interleave` is set. The close then
fails after five seconds, and the program exits with status 1. See
[Operations on a subscription session](../../docs/junos-verified.md#operations-on-a-subscription-session).
