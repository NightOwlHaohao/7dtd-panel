# Service override

- Service lifecycle states are stopped, starting, running, stopping, failed, and unknown; show the state as text plus its semantic color.
- During lifecycle work, disable conflicting controls and expose `aria-busy`; keep refresh available when safe.
- Stop/force-stop are danger actions with confirmation. Failed state includes the next recovery action and relevant log link.
